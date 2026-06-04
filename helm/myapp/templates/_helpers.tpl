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

{{/*
True only when an HPA will actually be created: autoscaling enabled AND the
metrics API is registered in the target cluster. If the metrics
API is absent we fall back to static replicas.
*/}}
{{- define "myapp.hpaActive" -}}
{{- and .Values.autoscaling.enabled (.Capabilities.APIVersions.Has "metrics.k8s.io/v1beta1") -}}
{{- end -}}

{{- define "myapp.podLabels" -}}
{{ include "myapp.selectorLabels" . }}
{{- range $k, $v := .Values.commonLabels }}
{{ $k }}: {{ $v }}
{{- end }}
{{- end -}}