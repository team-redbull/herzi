{{- define "herzi.name" -}}{{ default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}{{- end }}
{{- define "herzi.fullname" -}}
{{- if .Values.fullnameOverride }}{{ .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else if contains (include "herzi.name" .) .Release.Name }}{{ .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}{{ printf "%s-%s" .Release.Name (include "herzi.name" .) | trunc 63 | trimSuffix "-" }}{{- end }}
{{- end }}
{{- define "herzi.selectorLabels" -}}
app.kubernetes.io/name: {{ include "herzi.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
{{- define "herzi.labels" -}}
{{ include "herzi.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | quote }}
{{- end }}
{{- define "herzi.serviceAccount" -}}
{{- if .Values.serviceAccount.create }}{{ default (include "herzi.fullname" .) .Values.serviceAccount.name }}{{- else }}{{ default "default" .Values.serviceAccount.name }}{{- end }}
{{- end }}
{{- define "herzi.startupChecksum" -}}
{{- pick .Values.config "hubName" "agentNamespace" "installURL" "maxSlots" "stuckAfter" "temporalAddress" "temporalNamespace" "activityQueue" "workflowQueue" "segmentQueue" | toJson | sha256sum }}
{{- end }}
