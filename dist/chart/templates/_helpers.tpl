{{/*
The chart name.
*/}}
{{- define "mode-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
The prefix of every object's name: the release name plus the chart name, or
just the release name when it already contains the chart name.
*/}}
{{- define "mode-operator.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
An object name, <fullname>-<suffix>, kept within 63 characters. Takes a dict
with .suffix and .context.
*/}}
{{- define "mode-operator.resourceName" -}}
{{- $fullname := include "mode-operator.fullname" .context }}
{{- $maxLen := sub 62 (len .suffix) | int }}
{{- printf "%s-%s" (trunc $maxLen $fullname | trimSuffix "-") .suffix | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Labels for every object.
*/}}
{{- define "mode-operator.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "mode-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Labels that select the controller's pods.
*/}}
{{- define "mode-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "mode-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: controller
{{- end }}

{{/*
The controller's ServiceAccount, ClusterRole and Deployment name.
*/}}
{{- define "mode-operator.controllerName" -}}
{{- include "mode-operator.resourceName" (dict "suffix" "controller" "context" .) }}
{{- end }}
