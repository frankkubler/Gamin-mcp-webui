package cmd

import (
	"strings"

	"github.com/tamcore/garmin-mcp/internal/policy"
)

// tierNote names the scopes rather than describing them.
//
// "the matching OAuth scope" is the sentence an operator has to guess at, and a
// plausible guess exists: compat/tools.json spells a workout write's per-tool scope
// "garmin:workouts:write", which the client registry accepts, the metadata advertises
// and the policy then refuses. Naming the two the policy actually reads costs one line
// here and takes the guess away; the constants come from the policy itself, so a
// rename cannot leave this text behind.
func tierNote(remote bool) string {
	if remote {
		return "a write or destructive call also needs the OAuth scope " +
			string(policy.ScopeWrite) + " or " + string(policy.ScopeDestructive)
	}
	return "on stdio, each enabled tier is authorized by its operator flag"
}

// render formats the report for an operator.
//
// The layout is one fact per line, so a reader and a grep both find what they came
// for. Nothing here formats a secret: the effective configuration arrives already
// redacted, and every other field is a path, a label, or a bool.
func (d diagnosis) render() string {
	var b strings.Builder

	b.WriteString("garmin-mcp doctor\n\n")
	writeLine(&b, "transport", d.Transport)
	writeLine(&b, "region", d.Region)
	if !d.Remote {
		// A remote deployment binds no account in configuration: every principal
		// arrives with a request, so reporting one here would name a setting
		// nothing reads.
		writeLine(&b, "principal", d.PrincipalID+" ("+boundLabel(d.PrincipalBound)+")")
	}
	writeLine(&b, "state directory", d.StateDir)
	writeCheck(&b, "encryption key", d.KeyFile, d.KeyState, d.KeyDetail)
	if d.Remote {
		d.writeRemoteSection(&b)
	} else {
		writeCheck(&b, "token store", d.TokenDir, d.StoreState, d.StoreDetail)
		writeCheck(&b, "garmin tokens", "", d.TokensState, d.TokensDetail)
	}

	b.WriteString("\ntool tiers:\n")
	writeLine(&b, "  read-only", "always registered")
	writeLine(&b, "  write", enabledLabel(d.WriteEnabled))
	writeLine(&b, "  destructive", enabledLabel(d.DestructiveEnabled))
	b.WriteString("  note: " + tierNote(d.Remote) + "\n")

	b.WriteString("\neffective configuration:\n")
	b.WriteString(d.ConfigLine + "\n")
	return b.String()
}

// writeLine writes one "label: value" line.
func writeLine(b *strings.Builder, label, value string) {
	b.WriteString(label + ": " + value + "\n")
}

// writeCheck writes one check, with its location when it has one.
func writeCheck(b *strings.Builder, label, location string, checked state, detail string) {
	value := detail
	if location != "" {
		value = location + " — " + detail
	}
	writeLine(b, label, string(checked)+": "+value)
}

// boundLabel renders whether the principal is usable as one.
func boundLabel(bound bool) string {
	if bound {
		return "bound"
	}
	return "not a usable principal identifier"
}

// enabledLabel renders an operator enablement flag.
func enabledLabel(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}
