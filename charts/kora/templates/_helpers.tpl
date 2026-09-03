
{{/*
kora.bytes converts a Kubernetes quantity (128Mi, 2Gi, 512M, 1000000) into
a plain byte count, so two values written in different units can be compared.

Helm has no unit-aware comparison, and comparing the strings "8Gi" and "1Gi"
lexically gets the answer wrong. Only the suffixes Kubernetes actually accepts
for memory are handled; anything else is an error rather than a silent zero,
because a zero here would make a size check pass by default.
*/}}
{{- define "kora.bytes" -}}
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

{{/*
kora.gomemlimit takes a Kubernetes memory quantity (the container's memory
limit) and returns 90% of it as a bare byte count, for the GOMEMLIMIT env var.

Two things rule out the obvious alternatives. GOMEMLIMIT's own grammar is
`^[0-9]+(([KMGT]i)?B)?$` or the literal "off" -- "512Mi" is not valid input and
crashes the process at startup, so the value.yaml quantity cannot be passed
through unchanged. And the Downward API's resourceFieldRef, which does convert
a quantity to bytes for free, can only emit the limit itself: it has no way to
take a percentage of it, so using it means running right up against the limit
with no headroom for the parts of the address space GOMEMLIMIT does not
cover (goroutine stacks, off-heap allocations, the runtime itself).

90% comes from the Go GC guide's own recommendation to leave 5-10% headroom
under the container limit; this takes the larger, more conservative end of
that range. int64 on a positive mulf result truncates toward zero, which is a
floor here -- rounding up would risk landing above the 90% mark.
*/}}
{{- define "kora.gomemlimit" -}}
{{- $bytes := include "kora.bytes" . | int64 -}}
{{ mulf ($bytes | float64) 0.9 | int64 }}
{{- end -}}
