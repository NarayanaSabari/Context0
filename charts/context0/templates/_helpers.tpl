
{{/*
context0.bytes converts a Kubernetes quantity (128Mi, 2Gi, 512M, 1000000) into
a plain byte count, so two values written in different units can be compared.

Helm has no unit-aware comparison, and comparing the strings "8Gi" and "1Gi"
lexically gets the answer wrong. Only the suffixes Kubernetes actually accepts
for memory are handled; anything else is an error rather than a silent zero,
because a zero here would make a size check pass by default.
*/}}
{{- define "context0.bytes" -}}
{{- $v := . | toString -}}
{{- if hasSuffix "Ki" $v -}}{{ mulf (trimSuffix "Ki" $v | float64) 1024 | int64 }}
{{- else if hasSuffix "Mi" $v -}}{{ mulf (trimSuffix "Mi" $v | float64) 1048576 | int64 }}
{{- else if hasSuffix "Gi" $v -}}{{ mulf (trimSuffix "Gi" $v | float64) 1073741824 | int64 }}
{{- else if hasSuffix "Ti" $v -}}{{ mulf (trimSuffix "Ti" $v | float64) 1099511627776 | int64 }}
{{- else if hasSuffix "K" $v -}}{{ mulf (trimSuffix "K" $v | float64) 1000 | int64 }}
{{- else if hasSuffix "M" $v -}}{{ mulf (trimSuffix "M" $v | float64) 1000000 | int64 }}
{{- else if hasSuffix "G" $v -}}{{ mulf (trimSuffix "G" $v | float64) 1000000000 | int64 }}
{{- else if regexMatch "^[0-9]+$" $v -}}{{ $v | int64 }}
{{- else -}}{{ fail (printf "cannot parse %q as a memory quantity" $v) }}
{{- end -}}
{{- end -}}
