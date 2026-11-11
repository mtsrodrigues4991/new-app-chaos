{{- define "nac.labels" -}}
app.kubernetes.io/part-of: new-app-chaos
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
