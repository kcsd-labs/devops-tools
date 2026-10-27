// Package changes describes what an edit changed without saying what anything
// was changed to.
//
// It exists for the audit trail. A record that says only "updated" cannot
// answer the first question an incident review asks — what changed? — and one
// that records values turns everyone who can read the trail into someone who
// has seen every password in every secret and configuration file it covers.
// Names are the middle ground: which keys, which settings.
//
// Never values, and never anything derived from one. A hash of a short password
// is the password, given a few minutes; a length narrows the search. What this
// package returns is safe to write into a log precisely because there is
// nothing in it that came from a value.
package changes

import (
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// maxNames bounds each list in a description. A file rewritten from scratch
// would otherwise produce a record as long as the file, and the trail is read
// a screen at a time.
const maxNames = 10

// NoChange is what a save that changed nothing is described as. Worth saying:
// "saved, identical" and "saved, changed" are different answers to a review.
const NoChange = "no change"

// Keys describes the difference between two sets of named values: which names
// were added, which had their value changed, which were removed.
//
// Values are compared and then forgotten; only the names reach the result.
func Keys(before, after map[string]string) string {
	var added, changed, removed []string
	for k, v := range after {
		old, ok := before[k]
		switch {
		case !ok:
			added = append(added, k)
		case old != v:
			changed = append(changed, k)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			removed = append(removed, k)
		}
	}
	return describe(added, changed, removed)
}

// Document describes the difference between two versions of a configuration
// file. name is only used for its extension; existed false means the file is
// being created.
//
// YAML and JSON are compared setting by setting and reported as paths —
// datasource.pool.max — because that is how people think about these files.
// Properties and env files are compared key by key. Anything else is reported
// as a count of lines, which says how much changed without saying what to.
func Document(name, before, after string, existed bool) string {
	var d string
	switch strings.ToLower(path.Ext(name)) {
	case ".yaml", ".yml", ".json":
		b, berr := flatten(before)
		a, aerr := flatten(after)
		if berr == nil && aerr == nil {
			d = Keys(b, a)
		} else {
			// A file that does not parse is still a change worth describing,
			// and a parse error must not be the reason a record says nothing.
			d = lines(before, after)
		}
	case ".properties", ".env":
		d = Keys(properties(before), properties(after))
	default:
		d = lines(before, after)
	}
	if !existed {
		return "new file; " + d
	}
	return d
}

func describe(added, changed, removed []string) string {
	var parts []string
	if len(added) > 0 {
		parts = append(parts, "added "+list(added))
	}
	if len(changed) > 0 {
		parts = append(parts, "changed "+list(changed))
	}
	if len(removed) > 0 {
		parts = append(parts, "removed "+list(removed))
	}
	if len(parts) == 0 {
		return NoChange
	}
	return strings.Join(parts, "; ")
}

func list(names []string) string {
	sort.Strings(names)
	if len(names) <= maxNames {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:maxNames], ", ") + fmt.Sprintf(" and %d more", len(names)-maxNames)
}

// flatten turns a YAML or JSON document into one entry per setting, keyed by
// its dotted path. The values in the map are only ever compared with each
// other; they never leave this package.
//
// A list is one setting, not one per element: inserting at the front of a list
// would otherwise report every element after it as changed. Several documents
// in one file are told apart by their position, [0], [1] and so on.
func flatten(doc string) (map[string]string, error) {
	var docs []any
	dec := yaml.NewDecoder(strings.NewReader(doc))
	for {
		var v any
		err := dec.Decode(&v)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		docs = append(docs, v)
	}
	out := map[string]string{}
	for i, v := range docs {
		prefix := ""
		if len(docs) > 1 {
			prefix = fmt.Sprintf("[%d]", i)
		}
		walk(prefix, v, out)
	}
	return out, nil
}

func walk(at string, v any, out map[string]string) {
	switch m := v.(type) {
	case map[string]any:
		for k, child := range m {
			walk(join(at, k), child, out)
		}
	case map[any]any:
		for k, child := range m {
			walk(join(at, fmt.Sprint(k)), child, out)
		}
	case nil:
		if at != "" {
			out[at] = "null"
		}
	default:
		if at == "" {
			at = "(document)"
		}
		// The type is part of the comparison: "1" and 1 are different
		// settings to whatever reads the file.
		out[at] = fmt.Sprintf("%T:%v", v, v)
	}
}

func join(at, key string) string {
	if at == "" {
		return key
	}
	return at + "." + key
}

// properties reads key=value, key: value and key value lines, skipping comments
// and blank lines and joining lines continued with a trailing backslash. It is
// a reader for describing a change, not a full parser: escapes inside keys are
// rare enough in configuration that a key named slightly wrongly in an audit
// record costs nothing.
func properties(s string) map[string]string {
	out := map[string]string{}
	var carried string
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimLeft(strings.TrimRight(raw, "\r"), " \t")
		if carried != "" {
			line, carried = carried+line, ""
		}
		if line == "" || line[0] == '#' || line[0] == '!' {
			continue
		}
		if strings.HasSuffix(line, `\`) && !strings.HasSuffix(line, `\\`) {
			carried = strings.TrimSuffix(line, `\`)
			continue
		}
		i := strings.IndexAny(line, "=: \t")
		if i < 0 {
			out[line] = ""
			continue
		}
		out[strings.TrimSpace(line[:i])] = strings.TrimLeft(line[i+1:], " \t=:")
	}
	return out
}

// lines counts lines added and removed, as a multiset rather than a real diff:
// how much changed is the useful part, and a real diff would have to quote the
// lines to be any better than this.
func lines(before, after string) string {
	b, a := lineCounts(before), lineCounts(after)
	var added, removed int
	for l, n := range a {
		if d := n - b[l]; d > 0 {
			added += d
		}
	}
	for l, n := range b {
		if d := n - a[l]; d > 0 {
			removed += d
		}
	}
	if added == 0 && removed == 0 {
		return NoChange
	}
	return fmt.Sprintf("%d %s added, %d removed", added, plural(added, "line", "lines"), removed)
}

func lineCounts(s string) map[string]int {
	out := map[string]int{}
	// The final newline is how a file ends, not a line of it; the same rule
	// the drift check uses, so that a save that only added one is no change.
	s = strings.TrimRight(s, "\r\n")
	if s == "" {
		return out
	}
	for _, l := range strings.Split(s, "\n") {
		out[strings.TrimRight(l, "\r")]++
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
