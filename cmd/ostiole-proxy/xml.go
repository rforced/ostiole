package main

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"

	"github.com/corazawaf/coraza/v3/experimental/plugins"
	"github.com/corazawaf/coraza/v3/experimental/plugins/plugintypes"
)

// The WAF reads an XML body (ctl:requestBodyProcessor=XML) into XML:/*, its
// text, and XML://@*, its attribute values. Coraza's own reader closes
// HTML's empty elements as they open, so an XML-RPC <param> or an RSS <link>
// with an end tag does not parse, and the body is refused. This one is
// Coraza's without that, registered under the same name.
func init() {
	plugins.RegisterBodyProcessor("xml", func() plugintypes.BodyProcessor { return xmlBody{} })
}

type xmlBody struct{}

func (xmlBody) ProcessRequest(r io.Reader, v plugintypes.TransactionVariables, _ plugintypes.BodyProcessorOptions) error {
	attrs, text, err := readXML(r)
	if err != nil {
		return err
	}
	col := v.RequestXML()
	col.Set("//@*", attrs)
	col.Set("/*", text)
	return nil
}

func (xmlBody) ProcessResponse(io.Reader, plugintypes.TransactionVariables, plugintypes.BodyProcessorOptions) error {
	return nil
}

// readXML is every attribute value and every piece of text in a document,
// in order. It is as lenient as Coraza's: a missing end tag is made up, an
// unknown entity is left as it is, and a body cut off at the size limit is
// read up to the cut.
func readXML(r io.Reader) (attrs, text []string, err error) {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	for {
		tok, err := dec.Token()
		if err != nil && !errors.Is(err, io.EOF) && !cutOff(err) {
			return nil, nil, err
		}
		if tok == nil {
			return attrs, text, nil
		}
		switch t := tok.(type) {
		case xml.StartElement:
			for _, a := range t.Attr {
				attrs = append(attrs, a.Value)
			}
		case xml.CharData:
			if s := strings.TrimSpace(string(t)); s != "" {
				text = append(text, s)
			}
		}
	}
}

// cutOff reports a document that ends inside an element.
func cutOff(err error) bool {
	var syntax *xml.SyntaxError
	return errors.As(err, &syntax) && syntax.Msg == "unexpected EOF"
}
