{{/*
Expand the name of the chart.
*/}}
{{- define "go-zork.name" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "go-zork.labels" -}}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
app.kubernetes.io/name: {{ include "go-zork.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "go-zork.selectorLabels" -}}
app.kubernetes.io/name: {{ include "go-zork.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Container port for the selected mode
*/}}
{{- define "go-zork.containerPort" -}}
{{- if eq .Values.mode "mcp" -}}
{{ .Values.mcp.port }}
{{- else -}}
{{ .Values.web.port }}
{{- end }}
{{- end }}
