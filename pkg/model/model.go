// Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//     http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package model

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	ackfp "github.com/aws-controllers-k8s/code-generator/pkg/fieldpath"
	ackgenconfig "github.com/aws-controllers-k8s/code-generator/pkg/generate/config"
	"github.com/aws-controllers-k8s/code-generator/pkg/generate/templateset"
	"github.com/aws-controllers-k8s/code-generator/pkg/names"
	"github.com/aws-controllers-k8s/code-generator/pkg/util"
	awssdkmodel "github.com/aws/aws-sdk-go/private/model/api"
)

var (
	// ErrNilShapePointer indicates an unexpected nil Shape pointer
	ErrNilShapePointer = errors.New("found nil Shape pointer")
)

// Model contains the ACK model for the generator to process and apply
// templates against.
type Model struct {
	SDKAPI             *SDKAPI
	servicePackageName string
	apiVersion         string
	crds               []*CRD
	typeDefs           []*TypeDef
	typeImports        map[string]string
	typeRenames        map[string]string
	// Instructions to the code generator how to handle the API and its
	// resources
	cfg *ackgenconfig.Config
}

// MetaVars returns a MetaVars struct populated with metadata about the AWS
// service API
func (m *Model) MetaVars() templateset.MetaVars {
	return templateset.MetaVars{
		ServicePackageName:   m.servicePackageName,
		ServiceID:            m.SDKAPI.ServiceID(),
		ServiceModelName:     m.cfg.ModelName,
		APIGroup:             m.APIGroup(),
		APIVersion:           m.apiVersion,
		APIInterfaceTypeName: m.SDKAPI.APIInterfaceTypeName(),
		CRDNames:             m.crdNames(),
	}
}

// crdNames returns all crd names lowercased and in plural
func (m *Model) crdNames() []string {
	var crdConfigs []string

	crds, _ := m.GetCRDs()
	for _, crd := range crds {
		crdConfigs = append(crdConfigs, strings.ToLower(crd.Plural))
	}

	return crdConfigs
}

// GetCRDs returns a slice of `CRD` structs that describe the
// top-level resources discovered by the code generator for an AWS service API
func (m *Model) GetCRDs() ([]*CRD, error) {
	if m.crds != nil {
		return m.crds, nil
	}
	crds := []*CRD{}

	opMap := m.SDKAPI.GetOperationMap(m.cfg)

	createOps := (*opMap)[OpTypeCreate]
	readOneOps := (*opMap)[OpTypeGet]
	readManyOps := (*opMap)[OpTypeList]
	updateOps := (*opMap)[OpTypeUpdate]
	deleteOps := (*opMap)[OpTypeDelete]
	getAttributesOps := (*opMap)[OpTypeGetAttributes]
	setAttributesOps := (*opMap)[OpTypeSetAttributes]

	for crdName, createOp := range createOps {
		if m.cfg.IsIgnoredResource(crdName) {
			continue
		}
		crdNames := names.New(crdName)
		ops := Ops{
			Create:        createOps[crdName],
			ReadOne:       readOneOps[crdName],
			ReadMany:      readManyOps[crdName],
			Update:        updateOps[crdName],
			Delete:        deleteOps[crdName],
			GetAttributes: getAttributesOps[crdName],
			SetAttributes: setAttributesOps[crdName],
		}
		m.RemoveIgnoredOperations(&ops)
		crd := NewCRD(m.SDKAPI, m.cfg, crdNames, ops)

		// OK, begin to gather the CRDFields that will go into the Spec struct.
		// These fields are those members of the Create operation's Input
		// Shape.
		inputShape := createOp.InputRef.Shape
		if inputShape == nil {
			return nil, ErrNilShapePointer
		}
		for memberName, memberShapeRef := range inputShape.MemberRefs {
			if memberShapeRef.Shape == nil {
				return nil, ErrNilShapePointer
			}
			// Handles field renames, if applicable
			fieldName, _ := m.cfg.ResourceFieldRename(
				crd.Names.Original,
				createOp.Name,
				memberName,
			)
			memberNames := names.New(fieldName)
			memberNames.ModelOriginal = memberName
			if memberName == "Attributes" && m.cfg.UnpacksAttributesMap(crdName) {
				crd.UnpackAttributes()
				continue
			}
			crd.AddSpecField(memberNames, memberShapeRef)

			// If this field has ReferencesConfig, add corresponding
			// resource reference field in spec as well
			fConfig := m.cfg.ResourceFields(crdName)[fieldName]
			if fConfig != nil && fConfig.References != nil {
				referenceFieldNames := names.New(fieldName + "Ref")
				rf := NewReferenceField(crd, referenceFieldNames, memberShapeRef)
				crd.SpecFields[referenceFieldNames.Original] = rf
				crd.Fields[referenceFieldNames.Camel] = rf
			}
		}

		// Now any additional Spec fields that are required from other API
		// operations.
		for targetFieldName, fieldConfig := range m.cfg.ResourceFields(crdName) {
			if fieldConfig.IsReadOnly {
				// It's a Status field...
				continue
			}

			var found bool
			var memberShapeRef *awssdkmodel.ShapeRef

			if fieldConfig.From != nil {
				from := fieldConfig.From
				memberShapeRef, found = m.SDKAPI.GetInputShapeRef(
					from.Operation, from.Path,
				)
				if !found {
					// This is a compile-time failure, just bomb out...
					msg := fmt.Sprintf(
						"unknown additional Spec field with Op: %s and Path: %s",
						from.Operation, from.Path,
					)
					panic(msg)
				}
			} else if fieldConfig.CustomField != nil {
				customField := fieldConfig.CustomField
				if customField.ListOf != "" {
					memberShapeRef = m.SDKAPI.GetCustomShapeRef(customField.ListOf)
				} else {
					memberShapeRef = m.SDKAPI.GetCustomShapeRef(customField.MapOf)
				}
				if memberShapeRef == nil {
					// This is a compile-time failure, just bomb out...
					msg := fmt.Sprintf(
						"unknown additional Spec field with custom field %+v",
						customField,
					)
					panic(msg)
				}
			} else if fieldConfig.Type != nil {
				// We have a custom field that has a type override and has not
				// been inferred via the normal Create Input shape or via the
				// SourceFieldConfig. Manually construct the field and its
				// shape reference here.
				typeOverride := *fieldConfig.Type
				memberShapeRef = m.SDKAPI.GetShapeRefFromType(typeOverride)
			} else {
				// Spec field is not well defined
				continue
			}

			memberNames := names.New(targetFieldName)
			crd.AddSpecField(memberNames, memberShapeRef)
		}

		// Now process the fields that will go into the Status struct. We want
		// fields that are in the Create operation's Output Shape but that are
		// not in the Input Shape.
		outputShape, err := crd.GetOutputShape(createOp)
		if err != nil {
			return nil, err
		}
		if outputShape.UsedAsOutput && len(outputShape.MemberRefs) == 1 {
			// We might be in a "wrapper" shape. Unwrap it to find the real object
			// representation for the CRD's createOp. If there is a single member
			// shape and that member shape is a structure, unwrap it.
			for _, memberRef := range outputShape.MemberRefs {
				if memberRef.Shape.Type == "structure" {
					outputShape = memberRef.Shape
				}
			}
		}
		for memberName, memberShapeRef := range outputShape.MemberRefs {
			if memberShapeRef.Shape == nil {
				return nil, ErrNilShapePointer
			}
			// Check that the field in the output shape isn't the same as
			// fields in the input shape (handles field renames, if applicable)
			fieldName, _ := m.cfg.ResourceFieldRename(
				crd.Names.Original,
				createOp.Name,
				memberName,
			)
			if inSpec, _ := crd.HasMember(fieldName, createOp.Name); inSpec {
				// We don't put fields that are already in the Spec struct into
				// the Status struct
				continue
			}
			memberNames := names.New(fieldName)

			//TODO:(brycahta) should we support overriding these fields?
			if memberName == "Attributes" && m.cfg.UnpacksAttributesMap(crdName) {
				continue
			}
			if crd.IsPrimaryARNField(memberName) {
				// We automatically place the primary resource ARN value into
				// the Status.ACKResourceMetadata.ARN field
				continue
			}
			crd.AddStatusField(memberNames, memberShapeRef)
		}

		// Now add the additional Status fields that are required from other
		// API operations.
		for targetFieldName, fieldConfig := range m.cfg.ResourceFields(crdName) {
			if !fieldConfig.IsReadOnly {
				// It's a Spec field...
				continue
			}

			var found bool
			var memberShapeRef *awssdkmodel.ShapeRef

			if fieldConfig.From != nil {
				from := fieldConfig.From
				memberShapeRef, found = m.SDKAPI.GetOutputShapeRef(
					from.Operation, from.Path,
				)
				if !found {
					// This is a compile-time failure, just bomb out...
					msg := fmt.Sprintf(
						"unknown additional Status field with Op: %s and Path: %s",
						from.Operation, from.Path,
					)
					panic(msg)
				}
			} else if fieldConfig.CustomField != nil {
				customField := fieldConfig.CustomField
				if customField.ListOf != "" {
					memberShapeRef = m.SDKAPI.GetCustomShapeRef(customField.ListOf)
				} else {
					memberShapeRef = m.SDKAPI.GetCustomShapeRef(customField.MapOf)
				}
				if memberShapeRef == nil {
					// This is a compile-time failure, just bomb out...
					msg := fmt.Sprintf(
						"unknown additional Status field with custom field %+v",
						customField,
					)
					panic(msg)
				}
			} else {
				// Status field is not well defined
				continue
			}

			memberNames := names.New(targetFieldName)
			crd.AddStatusField(memberNames, memberShapeRef)
		}

		crds = append(crds, crd)
	}
	sort.Slice(crds, func(i, j int) bool {
		return crds[i].Names.Camel < crds[j].Names.Camel
	})
	// This is the place that we build out the CRD.Fields map with
	// `pkg/model.Field` objects that represent the non-top-level Spec and
	// Status fields.
	m.processFields(crds)
	m.crds = crds
	return crds, nil
}

// RemoveIgnoredOperations updates Ops argument by setting those
// operations to nil that are configured to be ignored in generator config for
// the AWS service
func (m *Model) RemoveIgnoredOperations(ops *Ops) {
	if m.cfg.IsIgnoredOperation(ops.Create) {
		ops.Create = nil
	}
	if m.cfg.IsIgnoredOperation(ops.ReadOne) {
		ops.ReadOne = nil
	}
	if m.cfg.IsIgnoredOperation(ops.ReadMany) {
		ops.ReadMany = nil
	}
	if m.cfg.IsIgnoredOperation(ops.Update) {
		ops.Update = nil
	}
	if m.cfg.IsIgnoredOperation(ops.Delete) {
		ops.Delete = nil
	}
	if m.cfg.IsIgnoredOperation(ops.GetAttributes) {
		ops.GetAttributes = nil
	}
	if m.cfg.IsIgnoredOperation(ops.SetAttributes) {
		ops.SetAttributes = nil
	}
}

// IsShapeUsedInCRDs returns true if the supplied shape name is a member of amy
// CRD's payloads or those payloads sub-member shapes
func (m *Model) IsShapeUsedInCRDs(shapeName string) bool {
	crds, _ := m.GetCRDs()
	for _, crd := range crds {
		if crd.HasShapeAsMember(shapeName) {
			return true
		}
	}
	return false
}

// GetTypeDefs returns a slice of `TypeDef` pointers
func (m *Model) GetTypeDefs() ([]*TypeDef, error) {
	if m.typeDefs != nil {
		return m.typeDefs, nil
	}

	tdefs := []*TypeDef{}
	// Map, keyed by original Shape GoTypeElem(), with the values being a
	// renamed type name (due to conflicting names)
	trenames := map[string]string{}

	payloads := m.SDKAPI.GetPayloads()

	for shapeName, shape := range m.SDKAPI.API.Shapes {
		if util.InStrings(shapeName, payloads) && !m.IsShapeUsedInCRDs(shapeName) {
			// Payloads are not type defs, unless explicitly used
			continue
		}
		if shape.Type != "structure" {
			continue
		}
		if shape.Exception {
			// Neither are exceptions
			continue
		}
		tdefNames := names.New(shapeName)
		if m.SDKAPI.HasConflictingTypeName(shapeName, m.cfg) {
			tdefNames.Camel += ConflictingNameSuffix
			trenames[shapeName] = tdefNames.Camel
		}

		attrs := map[string]*Attr{}
		for memberName, memberRef := range shape.MemberRefs {
			memberNames := names.New(memberName)
			memberShape := memberRef.Shape
			if !m.IsShapeUsedInCRDs(memberShape.ShapeName) {
				continue
			}
			gt := m.getShapeCleanGoType(memberShape)
			attrs[memberName] = NewAttr(memberNames, gt, memberShape)
		}
		if len(attrs) == 0 {
			// Just ignore these...
			continue
		}
		tdefs = append(tdefs, &TypeDef{
			Shape: shape,
			Names: tdefNames,
			Attrs: attrs,
		})
	}
	sort.Slice(tdefs, func(i, j int) bool {
		return tdefs[i].Names.Camel < tdefs[j].Names.Camel
	})
	m.processNestedFieldTypeDefs(tdefs)
	m.typeDefs = tdefs
	m.typeRenames = trenames
	return tdefs, nil
}

// getShapeCleanGoType returns a cleaned-up and Camel-cased GoType name for a given shape.
func (m *Model) getShapeCleanGoType(shape *awssdkmodel.Shape) string {
	switch shape.Type {
	case "map":
		// If it's a map type we need to set the GoType to the cleaned-up
		// Camel-cased name
		return "map[string]" + m.getShapeCleanGoType(shape.ValueRef.Shape)
	case "list", "array":
		// If it's a list type, we need to set the GoType to the cleaned-up
		// Camel-cased name
		return "[]" + m.getShapeCleanGoType(shape.MemberRef.Shape)
	case "timestamp":
		// time.Time needs to be converted to apimachinery/metav1.Time
		// otherwise there is no DeepCopy support
		return "*metav1.Time"
	case "structure":
		// There are shapes that are called things like DBProxyStatus that are
		// fields in a DBProxy CRD... we need to ensure the type names don't
		// conflict. Also, the name of the Go type in the generated code is
		// Camel-cased and normalized, so we use that as the Go type
		goType := shape.GoType()
		typeNames := names.New(goType)
		if m.SDKAPI.HasConflictingTypeName(goType, m.cfg) {
			typeNames.Camel += ConflictingNameSuffix
		}
		return "*" + typeNames.Camel
	default:
		return shape.GoType()
	}
}

// processNestedFieldTypeDefs updates the supplied TypeDef structs' if a nested
// field has been configured with a type overriding FieldConfig -- such as
// FieldConfig.IsSecret.
func (m *Model) processNestedFieldTypeDefs(
	tdefs []*TypeDef,
) {
	crds, _ := m.GetCRDs()
	for _, crd := range crds {
		for fieldPath, field := range crd.Fields {
			if !strings.Contains(fieldPath, ".") {
				// top-level fields have already had their structure
				// transformed during the CRD.AddSpecField and
				// CRD.AddStatusField methods. All we need to do here is look
				// at nested fields, which are identifiable as fields with
				// field paths contains a dot (".")
				continue
			}
			if field.FieldConfig == nil {
				// Likewise, we don't need to transform any TypeDef if the
				// nested field doesn't have a FieldConfig instructing us to
				// treat this field differently.
				continue
			}
			if field.FieldConfig.IsSecret {
				// Find the TypeDef that was created for the *containing*
				// secret field struct. For example, assume the nested field
				// path `Users..Password`, we'd want to find the TypeDef that
				// was created for the `Users` field's element type (which is a
				// struct)
				replaceSecretAttrGoType(crd, field, tdefs)
			}
		}
	}
}

// replaceSecretAttrGoType replaces a nested field Attr's GoType with
// `*ackv1alpha1.SecretKeyReference`.
func replaceSecretAttrGoType(
	crd *CRD,
	field *Field,
	tdefs []*TypeDef,
) {
	fieldPath := ackfp.FromString(field.Path)
	parentFieldPath := fieldPath.Copy()
	parentFieldPath.Pop()
	parentField, ok := crd.Fields[parentFieldPath.String()]
	if !ok {
		msg := fmt.Sprintf(
			"Cannot find parent field at parent path %s for %s",
			parentFieldPath,
			fieldPath,
		)
		panic(msg)
	}
	if parentField.ShapeRef == nil {
		msg := fmt.Sprintf(
			"parent field at parent path %s has a nil ShapeRef!",
			parentFieldPath,
		)
		panic(msg)
	}
	parentFieldShape := parentField.ShapeRef.Shape
	parentFieldShapeName := parentField.ShapeRef.ShapeName
	parentFieldShapeType := parentFieldShape.Type
	// For list and map types, we need to grab the element/value
	// type, since that's the type def we need to modify.
	if parentFieldShapeType == "list" {
		if parentFieldShape.MemberRef.Shape.Type != "structure" {
			msg := fmt.Sprintf(
				"parent field at parent path %s is a list type with a non-structure element member shape %s!",
				parentFieldPath,
				parentFieldShape.MemberRef.Shape.Type,
			)
			panic(msg)
		}
		parentFieldShapeName = parentField.ShapeRef.Shape.MemberRef.ShapeName
	} else if parentFieldShapeType == "map" {
		if parentFieldShape.ValueRef.Shape.Type != "structure" {
			msg := fmt.Sprintf(
				"parent field at parent path %s is a map type with a non-structure value member shape %s!",
				parentFieldPath,
				parentFieldShape.ValueRef.Shape.Type,
			)
			panic(msg)
		}
		parentFieldShapeName = parentField.ShapeRef.Shape.ValueRef.ShapeName
	}
	var parentTypeDef *TypeDef
	for _, tdef := range tdefs {
		if tdef.Names.Original == parentFieldShapeName {
			parentTypeDef = tdef
		}
	}
	if parentTypeDef == nil {
		msg := fmt.Sprintf(
			"unable to find associated TypeDef for parent field "+
				"at parent path %s!",
			parentFieldPath,
		)
		panic(msg)
	}
	// Now we modify the parent type def's Attr that corresponds to
	// the secret field...
	attr, found := parentTypeDef.Attrs[field.Names.Camel]
	if !found {
		msg := fmt.Sprintf(
			"unable to find attr %s in parent TypeDef %s "+
				"at parent path %s!",
			field.Names.Camel,
			parentTypeDef.Names.Original,
			parentFieldPath,
		)
		panic(msg)
	}
	attr.GoType = "*ackv1alpha1.SecretKeyReference"
}

// processFields is responsible for walking all of the CRDs' Spec and
// Status fields' Shape objects and adding `pkg/model.Field` objects for all
// nested fields along with that `Field`'s `Config` object that allows us to
// determine if the TypeDef associated with that nested field should have its
// data type overridden (e.g. for SecretKeyReferences)
func (m *Model) processFields(crds []*CRD) {
	for _, crd := range crds {
		for _, field := range crd.SpecFields {
			m.processTopLevelField(crd, field)
		}
		for _, field := range crd.StatusFields {
			m.processTopLevelField(crd, field)
		}
	}
}

// processTopLevelField processes any nested fields (non-scalar fields associated
// with the Spec and Status objects)
func (m *Model) processTopLevelField(
	crd *CRD,
	field *Field,
) {
	if field.ShapeRef == nil && !field.IsReference() && (field.FieldConfig == nil || !field.FieldConfig.IsAttribute) {
		fmt.Printf(
			"WARNING: Field %s:%s has nil ShapeRef and is not defined as an Attribute-based Field!\n",
			crd.Names.Original,
			field.Names.Original,
		)
		return
	}
	if field.ShapeRef != nil {
		fieldShape := field.ShapeRef.Shape
		fieldType := fieldShape.Type
		switch fieldType {
		case "structure":
			m.processStructField(crd, field.Path+".", field)
		case "list":
			m.processListField(crd, field.Path+".", field)
		case "map":
			m.processMapField(crd, field.Path+".", field)
		}
	}
}

// processField adds a new Field definition for a field within the CR
func (m *Model) processField(
	crd *CRD,
	parentFieldPath string,
	parentField *Field,
	fieldName string,
	fieldShapeRef *awssdkmodel.ShapeRef,
) {
	fieldConfigs := crd.Config().ResourceFields(crd.Names.Original)
	fieldNames := names.New(fieldName)
	fieldShape := fieldShapeRef.Shape
	fieldShapeType := fieldShape.Type
	fieldPath := parentFieldPath + fieldNames.Camel
	fieldConfig := fieldConfigs[fieldPath]
	field := NewField(crd, fieldPath, fieldNames, fieldShapeRef, fieldConfig)
	switch fieldShapeType {
	case "structure":
		m.processStructField(crd, fieldPath+".", field)
	case "list":
		m.processListField(crd, fieldPath+".", field)
	case "map":
		m.processMapField(crd, fieldPath+".", field)
	}
	crd.Fields[fieldPath] = field
}

// processStructField recurses through the members of a nested field that
// is a struct type and adds any Field objects to the supplied CRD.
func (m *Model) processStructField(
	crd *CRD,
	fieldPath string,
	field *Field,
) {
	fieldShape := field.ShapeRef.Shape
	for memberName, memberRef := range fieldShape.MemberRefs {
		m.processField(crd, fieldPath, field, memberName, memberRef)
	}
}

// processListField recurses through the members of a nested field that
// is a list type that has a struct element type and adds any Field objects to
// the supplied CRD.
func (m *Model) processListField(
	crd *CRD,
	fieldPath string,
	field *Field,
) {
	fieldShape := field.ShapeRef.Shape
	elementFieldShape := fieldShape.MemberRef.Shape
	if elementFieldShape.Type != "structure" {
		return
	}
	for memberName, memberRef := range elementFieldShape.MemberRefs {
		m.processField(crd, fieldPath, field, memberName, memberRef)
	}
}

// processMapField recurses through the members of a nested field that
// is a map type that has a struct value type and adds any Field objects to
// the supplied CRD.
func (m *Model) processMapField(
	crd *CRD,
	fieldPath string,
	field *Field,
) {
	fieldShape := field.ShapeRef.Shape
	valueFieldShape := fieldShape.ValueRef.Shape
	if valueFieldShape.Type != "structure" {
		return
	}
	for memberName, memberRef := range valueFieldShape.MemberRefs {
		m.processField(crd, fieldPath, field, memberName, memberRef)
	}
}

// GetEnumDefs returns a slice of pointers to `EnumDef` structs which
// represent string fields whose value is constrained to one or more specific
// string values.
func (m *Model) GetEnumDefs() ([]*EnumDef, error) {
	edefs := []*EnumDef{}

	for shapeName, shape := range m.SDKAPI.API.Shapes {
		if !shape.IsEnum() {
			continue
		}
		enumNames := names.New(shapeName)
		// Handle name conflicts with top-level CRD.Spec or CRD.Status
		// types
		if m.SDKAPI.HasConflictingTypeName(shapeName, m.cfg) {
			enumNames.Camel += ConflictingNameSuffix
		}
		edef, err := NewEnumDef(enumNames, shape.Enum)
		if err != nil {
			return nil, err
		}
		edefs = append(edefs, edef)
	}
	sort.Slice(edefs, func(i, j int) bool {
		return edefs[i].Names.Camel < edefs[j].Names.Camel
	})
	return edefs, nil
}

// ApplyShapeIgnoreRules removes the ignored shapes and fields from the API object
// so that they are not considered in any of the calculations of code generator.
func (m *Model) ApplyShapeIgnoreRules() {
	if m.cfg == nil || m.SDKAPI == nil {
		return
	}
	for sdkShapeID, shape := range m.SDKAPI.API.Shapes {
		for _, sn := range m.cfg.Ignore.ShapeNames {
			if shape.ShapeName == sn {
				delete(m.SDKAPI.API.Shapes, sdkShapeID)
				continue
			}
			// NOTE(muvaf): We need to remove the usage of the shape as well.
			for sdkMemberID, memberRef := range shape.MemberRefs {
				if memberRef.ShapeName == sn {
					delete(shape.MemberRefs, sdkMemberID)
				}
			}
		}
	}
	for _, fieldpath := range m.cfg.Ignore.FieldPaths {
		fp := ackfp.FromString(fieldpath)
		sn := fp.At(0)
		if shape, found := m.SDKAPI.API.Shapes[sn]; !found {
			msg := fmt.Sprintf(
				"referred to unknown shape %s in config's Ignore.FieldPaths", sn,
			)
			panic(msg)
		} else {
			// This is just some tomfoolery to make the Input and Output shapes
			// into ShapeRefs because the SDKAPI.Shapes is a map of Shape
			// pointers not a map of ShapeRefs...
			wrapper := &awssdkmodel.ShapeRef{
				ShapeName: sn,
				Shape:     shape,
			}
			// The last element of the fieldpath is the field/shape we want to
			// ignore...
			ignoreShape := fp.Pop()
			parentShapeRef := fp.ShapeRef(wrapper)
			// OK, now we delete the ignored shape by removing the shape from
			// the parent's member references...
			delete(parentShapeRef.Shape.MemberRefs, ignoreShape)
		}
	}
}

// GetConfig returns the configuration option used to define the current
// generator.
func (m *Model) GetConfig() *ackgenconfig.Config {
	return m.cfg
}

// APIGroup returns the normalized Kubernetes APIGroup for the AWS service API,
// e.g. "sns.services.k8s.aws"
func (m *Model) APIGroup() string {
	suffix := "services.k8s.aws"
	if m.SDKAPI.APIGroupSuffix != "" {
		suffix = m.SDKAPI.APIGroupSuffix
	}
	return fmt.Sprintf("%s.%s", m.servicePackageName, suffix)
}

// New returns a new Model struct for a supplied API model.
// Optionally, pass a file path to a generator config file that can be used to
// instruct the code generator how to handle the API properly
func New(
	SDKAPI *SDKAPI,
	servicePackageName string,
	apiVersion string,
	cfg ackgenconfig.Config,
) (*Model, error) {
	m := &Model{
		SDKAPI:             SDKAPI,
		servicePackageName: servicePackageName,
		apiVersion:         apiVersion,
		cfg:                &cfg,
	}
	m.ApplyShapeIgnoreRules()
	return m, nil
}
