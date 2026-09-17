{{- define "ufei.name" -}}
{{- printf "%s-ufei" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- define "ufei.selector" -}}
app.kubernetes.io/name: ufei
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
