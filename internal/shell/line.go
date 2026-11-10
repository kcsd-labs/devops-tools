// Package shell holds what a terminal session in a pod is allowed to send, and
// how it is written down.
//
// The session is a mirror: nothing reaches the container but whole lines the
// portal has already recorded. That only holds if the recorded line is what
// the shell then runs, so the line is plain text — no control characters a
// line editor would act on, and no line breaks that would make it two
// commands under one record.
package shell

import (
	"errors"
	"regexp"
	"unicode"
	"unicode/utf8"
)

// MaxLine is the longest line accepted. Long enough for any command a person
// types or pastes; a paste beyond it is a file, and belongs in one.
const MaxLine = 8 << 10

var (
	ErrLineTooLong = errors.New("the line is longer than 8 KiB")
	ErrLineControl = errors.New("the line holds a control character; a line is sent as typed text only")
	ErrLineUTF8    = errors.New("the line is not valid UTF-8")
)

// CheckLine refuses a line that would not run as recorded.
//
// A control character is refused rather than stripped: Tab completes, Escape
// sequences recall history or move the cursor, Ctrl+U and Ctrl+W erase. Any
// of them would make the shell run something other than the text in the
// trail. Interrupt and end-of-input have their own messages, and their own
// records.
func CheckLine(line string) error {
	if len(line) > MaxLine {
		return ErrLineTooLong
	}
	if !utf8.ValidString(line) {
		return ErrLineUTF8
	}
	for _, r := range line {
		if unicode.IsControl(r) {
			return ErrLineControl
		}
	}
	return nil
}

// Keys a session may send besides lines, and the byte the terminal gets.
var Keys = map[string]byte{
	"c": 0x03, // interrupt
	"d": 0x04, // end of input
}

// masks hide the values of credentials written into a command. The command is
// recorded — that is the point of the record — but the trail is read by
// everyone with audit-read in the namespace, and a password typed on the
// command line should not become one they have all seen.
//
// They catch the common forms. A credential in some other shape is recorded as
// typed, which is a reason to keep credentials out of commands.
var masks = []struct {
	re   *regexp.Regexp
	with string
}{
	// password=..., token=..., PGPASSWORD=..., api_key=...
	{regexp.MustCompile(`(?i)((?:pass(?:word|wd)?|pwd|token|secret|api[_-]?key|access[_-]?key)\s*[=:]\s*)("[^"]*"|'[^']*'|\S+)`), "${1}***"},
	// --password value, --token=value
	{regexp.MustCompile(`(?i)(--(?:pass(?:word)?|token|secret|api-key)[= ])("[^"]*"|'[^']*'|\S+)`), "${1}***"},
	// Authorization: Bearer value, as in curl -H
	{regexp.MustCompile(`(?i)(authorization:\s*(?:bearer|basic|token)?\s*)("[^"]*"|'[^']*'|[^\s"']+)`), "${1}***"},
	// mysql -pSECRET, the password glued to the flag
	{regexp.MustCompile(`(?i)(\b(?:mysql|mysqldump|mysqladmin|mariadb)\b[^|;&]*?\s-p)(\S+)`), "${1}***"},
	// scheme://user:password@host
	{regexp.MustCompile(`(://[^/\s:@]+:)([^@\s]+)(@)`), "${1}***${3}"},
}

// Mask returns the line with the values of recognisable credentials replaced.
func Mask(line string) string {
	for _, m := range masks {
		line = m.re.ReplaceAllString(line, m.with)
	}
	return line
}
