{{- define "herzi.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
{{- define "herzi.labels" -}}
{{ include "herzi.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | quote }}
{{- end }}
{{- define "herzi.serviceAccount" -}}
{{- if .Values.serviceAccount.create }}{{ default (.Release.Name) .Values.serviceAccount.name }}{{- else }}{{ default "default" .Values.serviceAccount.name }}{{- end }}
{{- end }}
{{- define "herzi.startupChecksum" -}}
{{- pick .Values.config "hubName" "agentNamespace" "installURL" "maxSlots" "stuckAfter" "temporalAddress" "temporalNamespace" "activityQueue" "workflowQueue" "segmentQueue" | toJson | sha256sum }}
{{- end }}
