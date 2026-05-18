{{- define "myapp.labels" -}}
app: {{ .Release.Name }}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- range $k, $v := .Values.commonLabels }}
{{ $k }}: {{ $v }}
{{- end }}
{{- end -}}

{{- define "myapp.selectorLabels" -}}
app: {{ .Release.Name }}
{{- end -}}

{{- define "myapp.podLabels" -}}
{{ include "myapp.selectorLabels" . }}
{{- range $k, $v := .Values.commonLabels }}
{{ $k }}: {{ $v }}
{{- end }}
{{- end -}}