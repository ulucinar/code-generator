apiVersion: operators.coreos.com/v1alpha1
kind: ClusterServiceVersion
metadata:
  annotations:
    categories: "Cloud Provider"
    alm-examples: '[]'
    capabilities: {{.Annotations.CapabilityLevel}}
    operatorframework.io/suggested-namespace: "ack-system"
    repository: {{.Annotations.Repository}}
    containerImage: {{.Annotations.ContainerImage}}/{{.ServicePackageName}}-controller:v{{.Version}}
    description: {{.Annotations.ShortDescription}}
    createdAt: {{.CreatedAt}}
    support: {{.Annotations.Support}}
    certified: {{.Annotations.IsCertified}}
  name: ack-{{.ServicePackageName }}-controller.v0.0.0
  namespace: placeholder
spec:
  apiservicedefinitions: {}
  customresourcedefinitions:
    owned: 
    {{- range .CRDs}}
    - kind: {{ .Kind}}
      name: {{ ToLower .Plural }}.{{$.APIGroup}}
      version: {{$.APIVersion}}
      displayName: {{.Kind}}
      description: {{.Kind}} represents the state of an AWS {{$.ServicePackageName}} {{.Kind}} resource.
    {{- end}}
  description: '{{ .Description }}'
  displayName: {{ .DisplayName}}
  icon:
  {{- range .Icon}}
  - base64data: {{ .Data }}
    mediatype: {{ .MediaType }}
  {{- end}}
  install:
    spec:
      deployments: null
    strategy: ""
  installModes:
  {{- range .InstallModes}}
  - supported: {{ .Supported }}
    type: {{ .Type }}
  {{- end}}
  keywords:
  - {{.ServicePackageName}}
  {{- range .Common.Keywords}}
  - {{ . }}
  {{- end}}
  {{- range .Keywords}}
  - {{ . }}
  {{- end}}
  links:
  {{- range .Common.Links}}
  - name: {{ .Name }}
    url: {{ .URL }}
  {{- end}}
  {{- range .Links}}
  - name: {{ .Name }}
    url: {{ .URL }}
  {{- end}}
  maintainers:
  {{- range .Maintainers}}
  - email: {{ .Email }}
    name: {{ .Name}}
  {{- end}}
  maturity: {{.Maturity}}
  provider:
    name: {{ .Provider.Name }}
    url: {{ .Provider.URL }}
  version: 0.0.0