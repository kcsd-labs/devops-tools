package shell

import (
	"strings"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/ianaindex"
)

// Charset is the character set a container's locale writes in, when it is not
// UTF-8. A shell in an image set to ru_RU.CP1251 prints its messages in
// Windows-1251; a browser terminal reads UTF-8, and drops every byte that is
// not. So the output is converted on its way out and typed lines on their way
// in — the container keeps the locale its application runs in.
type Charset struct {
	Name string
	enc  encoding.Encoding
}

// LookupCharset returns the conversion for a `locale charmap` answer, or nil
// when none is needed or the name is not one this knows: UTF-8 is passed as
// it is, and so is plain ASCII, which is a part of it.
func LookupCharset(charmap string) *Charset {
	name := strings.TrimSpace(charmap)
	switch strings.ToUpper(strings.ReplaceAll(name, "-", "")) {
	case "", "UTF8", "ANSI_X3.41968", "ASCII", "USASCII", "POSIX":
		return nil
	}
	enc, err := htmlindex.Get(name)
	if err != nil {
		if enc, err = ianaindex.IANA.Encoding(name); err != nil || enc == nil {
			return nil
		}
	}
	return &Charset{Name: name, enc: enc}
}

// Decoder turns the container's output into UTF-8.
func (c *Charset) Decoder() *encoding.Decoder { return c.enc.NewDecoder() }

// Encode turns a typed line into the container's character set. A character
// the set has no place for is refused rather than replaced: replaced, the
// shell would run something other than the line on record.
func (c *Charset) Encode(line string) (string, error) {
	return c.enc.NewEncoder().String(line)
}
