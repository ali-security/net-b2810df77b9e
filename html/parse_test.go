// Copyright 2010 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package html

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html/atom"
)

type testAttrs struct {
	text, want, context string
	scripting           bool
}

// readParseTest reads a single test case from r.
func readParseTest(r *bufio.Reader) (*testAttrs, error) {
	ta := &testAttrs{scripting: true}
	line, err := r.ReadSlice('\n')
	if err != nil {
		return nil, err
	}
	var b []byte

	// Read the HTML.
	if string(line) != "#data\n" {
		return nil, fmt.Errorf(`got %q want "#data\n"`, line)
	}
	for {
		line, err = r.ReadSlice('\n')
		if err != nil {
			return nil, err
		}
		if line[0] == '#' {
			break
		}
		b = append(b, line...)
	}
	ta.text = strings.TrimSuffix(string(b), "\n")
	b = b[:0]

	// Skip the error list.
	if string(line) != "#errors\n" {
		return nil, fmt.Errorf(`got %q want "#errors\n"`, line)
	}
	for {
		line, err = r.ReadSlice('\n')
		if err != nil {
			return nil, err
		}
		if line[0] == '#' {
			break
		}
	}

	// Skip the new-errors list.
	if string(line) == "#new-errors\n" {
		for {
			line, err = r.ReadSlice('\n')
			if err != nil {
				return nil, err
			}
			if line[0] == '#' {
				break
			}
		}
	}

	if ls := string(line); strings.HasPrefix(ls, "#script-") {
		switch {
		case strings.HasSuffix(ls, "-on\n"):
			ta.scripting = true
		case strings.HasSuffix(ls, "-off\n"):
			ta.scripting = false
		default:
			return nil, fmt.Errorf(`got %q, want "#script-on" or "#script-off"`, line)
		}
		for {
			line, err = r.ReadSlice('\n')
			if err != nil {
				return nil, err
			}
			if line[0] == '#' {
				break
			}
		}
	}

	if string(line) == "#document-fragment\n" {
		line, err = r.ReadSlice('\n')
		if err != nil {
			return nil, err
		}
		ta.context = strings.TrimSpace(string(line))
		line, err = r.ReadSlice('\n')
		if err != nil {
			return nil, err
		}
	}

	// Read the dump of what the parse tree should be.
	if string(line) != "#document\n" {
		return nil, fmt.Errorf(`got %q want "#document\n"`, line)
	}
	inQuote := false
	for {
		line, err = r.ReadSlice('\n')
		if err != nil && err != io.EOF {
			return nil, err
		}
		trimmed := bytes.Trim(line, "| \n")
		if len(trimmed) > 0 {
			if line[0] == '|' && trimmed[0] == '"' {
				inQuote = true
			}
			if trimmed[len(trimmed)-1] == '"' && !(line[0] == '|' && len(trimmed) == 1) {
				inQuote = false
			}
		}
		if len(line) == 0 || len(line) == 1 && line[0] == '\n' && !inQuote {
			break
		}
		b = append(b, line...)
	}
	ta.want = string(b)
	return ta, nil
}

func dumpIndent(w io.Writer, level int) {
	io.WriteString(w, "| ")
	for i := 0; i < level; i++ {
		io.WriteString(w, "  ")
	}
}

type sortedAttributes []Attribute

func (a sortedAttributes) Len() int {
	return len(a)
}

func (a sortedAttributes) Less(i, j int) bool {
	if a[i].Namespace != a[j].Namespace {
		return a[i].Namespace < a[j].Namespace
	}
	return a[i].Key < a[j].Key
}

func (a sortedAttributes) Swap(i, j int) {
	a[i], a[j] = a[j], a[i]
}

func dumpLevel(w io.Writer, n *Node, level int) error {
	dumpIndent(w, level)
	level++
	switch n.Type {
	case ErrorNode:
		return errors.New("unexpected ErrorNode")
	case DocumentNode:
		return errors.New("unexpected DocumentNode")
	case ElementNode:
		if n.Namespace != "" {
			fmt.Fprintf(w, "<%s %s>", n.Namespace, n.Data)
		} else {
			fmt.Fprintf(w, "<%s>", n.Data)
		}
		attr := sortedAttributes(n.Attr)
		sort.Sort(attr)
		for _, a := range attr {
			io.WriteString(w, "\n")
			dumpIndent(w, level)
			if a.Namespace != "" {
				fmt.Fprintf(w, `%s %s="%s"`, a.Namespace, a.Key, a.Val)
			} else {
				fmt.Fprintf(w, `%s="%s"`, a.Key, a.Val)
			}
		}
		if n.Namespace == "" && n.DataAtom == atom.Template {
			io.WriteString(w, "\n")
			dumpIndent(w, level)
			level++
			io.WriteString(w, "content")
		}
	case TextNode:
		fmt.Fprintf(w, `"%s"`, n.Data)
	case CommentNode:
		fmt.Fprintf(w, "<!-- %s -->", n.Data)
	case DoctypeNode:
		fmt.Fprintf(w, "<!DOCTYPE %s", n.Data)
		if n.Attr != nil {
			var p, s string
			for _, a := range n.Attr {
				switch a.Key {
				case "public":
					p = a.Val
				case "system":
					s = a.Val
				}
			}
			if p != "" || s != "" {
				fmt.Fprintf(w, ` "%s"`, p)
				fmt.Fprintf(w, ` "%s"`, s)
			}
		}
		io.WriteString(w, ">")
	case scopeMarkerNode:
		return errors.New("unexpected scopeMarkerNode")
	default:
		return errors.New("unknown node type")
	}
	io.WriteString(w, "\n")
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if err := dumpLevel(w, c, level); err != nil {
			return err
		}
	}
	return nil
}

func dump(n *Node) (string, error) {
	if n == nil || n.FirstChild == nil {
		return "", nil
	}
	var b bytes.Buffer
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if err := dumpLevel(&b, c, 0); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}

var testDataDirs = []string{"testdata/webkit/", "testdata/go/"}

func TestParser(t *testing.T) {
	for _, testDataDir := range testDataDirs {
		testFiles, err := filepath.Glob(testDataDir + "*.dat")
		if err != nil {
			t.Fatal(err)
		}
		for _, tf := range testFiles {
			t.Run(tf, func(t *testing.T) {
				f, err := os.Open(tf)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				r := bufio.NewReader(f)

				for i := 0; ; i++ {
					ta, err := readParseTest(r)
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					if parseTestBlacklist[ta.text] {
						continue
					}

					t.Run(fmt.Sprint(i), func(t *testing.T) {
						err = testParseCase(ta.text, ta.want, ta.context, ParseOptionEnableScripting(ta.scripting))

						if err != nil {
							t.Errorf("%s test #%d %q, %s", tf, i, ta.text, err)
						}
					})
				}
			})
		}
	}
}

// Issue 16318
func TestParserWithoutScripting(t *testing.T) {
	text := `<noscript><img src='https://golang.org/doc/gopher/frontpage.png' /></noscript><p><img src='https://golang.org/doc/gopher/doc.png' /></p>`
	want := `| <html>
|   <head>
|     <noscript>
|   <body>
|     <img>
|       src="https://golang.org/doc/gopher/frontpage.png"
|     <p>
|       <img>
|         src="https://golang.org/doc/gopher/doc.png"
`

	if err := testParseCase(text, want, "", ParseOptionEnableScripting(false)); err != nil {
		t.Errorf("test with scripting is disabled, %q, %s", text, err)
	}
}

// testParseCase tests one test case from the test files. If the test does not
// pass, it returns an error that explains the failure.
// text is the HTML to be parsed, want is a dump of the correct parse tree,
// and context is the name of the context node, if any.
func testParseCase(text, want, context string, opts ...ParseOption) (err error) {
	defer func() {
		if x := recover(); x != nil {
			switch e := x.(type) {
			case error:
				err = e
			default:
				err = fmt.Errorf("%v", e)
			}
		}
	}()

	var doc *Node
	if context == "" {
		doc, err = ParseWithOptions(strings.NewReader(text), opts...)
		if err != nil {
			return err
		}
	} else {
		namespace := ""
		if i := strings.IndexByte(context, ' '); i >= 0 {
			namespace, context = context[:i], context[i+1:]
		}
		contextNode := &Node{
			Data:      context,
			DataAtom:  atom.Lookup([]byte(context)),
			Namespace: namespace,
			Type:      ElementNode,
		}
		nodes, err := ParseFragmentWithOptions(strings.NewReader(text), contextNode, opts...)
		if err != nil {
			return err
		}
		doc = &Node{
			Type: DocumentNode,
		}
		for _, n := range nodes {
			doc.AppendChild(n)
		}
	}

	if err := checkTreeConsistency(doc); err != nil {
		return err
	}

	got, err := dump(doc)
	if err != nil {
		return err
	}
	// Compare the parsed tree to the #document section.
	if got != want {
		return fmt.Errorf("got vs want:\n----\n%s----\n%s----", got, want)
	}

	if renderTestBlacklist[text] || context != "" {
		return nil
	}

	// Check that rendering and re-parsing results in an identical tree.
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(Render(pw, doc))
	}()
	doc1, err := ParseWithOptions(pr, opts...)
	if err != nil {
		return err
	}
	got1, err := dump(doc1)
	if err != nil {
		return err
	}
	if got != got1 {
		return fmt.Errorf("got vs got1:\n----\n%s----\n%s----", got, got1)
	}

	return nil
}

// Some test inputs are simply skipped - we would otherwise fail the test. We
// blacklist such inputs from the parse test.
var parseTestBlacklist = map[string]bool{
	// See the a.Template TODO in inHeadIM.
	`<math><template><mo><template>`:                                     true,
	`<template><svg><foo><template><foreignObject><div></template><div>`: true,
}

// Some test input result in parse trees are not 'well-formed' despite
// following the HTML5 recovery algorithms. Rendering and re-parsing such a
// tree will not result in an exact clone of that tree. We blacklist such
// inputs from the render test.
var renderTestBlacklist = map[string]bool{
	// The second <a> will be reparented to the first <table>'s parent. This
	// results in an <a> whose parent is an <a>, which is not 'well-formed'.
	`<a><table><td><a><table></table><a></tr><a></table><b>X</b>C<a>Y`: true,
	// The same thing with a <p>:
	`<p><table></p>`: true,
	// More cases of <a> being reparented:
	`<a href="blah">aba<table><a href="foo">br<tr><td></td></tr>x</table>aoe`: true,
	`<a><table><a></table><p><a><div><a>`:                                     true,
	`<a><table><td><a><table></table><a></tr><a></table><a>`:                  true,
	`<template><a><table><a>`:                                                 true,
	// A similar reparenting situation involving <nobr>:
	`<!DOCTYPE html><body><b><nobr>1<table><nobr></b><i><nobr>2<nobr></i>3`: true,
	// A <plaintext> element is reparented, putting it before a table.
	// A <plaintext> element can't have anything after it in HTML.
	`<table><plaintext><td>`:                                   true,
	`<!doctype html><table><plaintext></plaintext>`:            true,
	`<!doctype html><table><tbody><plaintext></plaintext>`:     true,
	`<!doctype html><table><tbody><tr><plaintext></plaintext>`: true,
	// A form inside a table inside a form doesn't work either.
	`<!doctype html><form><table></form><form></table></form>`: true,
	// A script that ends at EOF may escape its own closing tag when rendered.
	`<!doctype html><script><!--<script `:          true,
	`<!doctype html><script><!--<script <`:         true,
	`<!doctype html><script><!--<script <a`:        true,
	`<!doctype html><script><!--<script </`:        true,
	`<!doctype html><script><!--<script </s`:       true,
	`<!doctype html><script><!--<script </script`:  true,
	`<!doctype html><script><!--<script </scripta`: true,
	`<!doctype html><script><!--<script -`:         true,
	`<!doctype html><script><!--<script -a`:        true,
	`<!doctype html><script><!--<script -<`:        true,
	`<!doctype html><script><!--<script --`:        true,
	`<!doctype html><script><!--<script --a`:       true,
	`<!doctype html><script><!--<script --<`:       true,
	`<script><!--<script `:                         true,
	`<script><!--<script <a`:                       true,
	`<script><!--<script </script`:                 true,
	`<script><!--<script </scripta`:                true,
	`<script><!--<script -`:                        true,
	`<script><!--<script -a`:                       true,
	`<script><!--<script --`:                       true,
	`<script><!--<script --a`:                      true,
	`<script><!--<script <`:                        true,
	`<script><!--<script </`:                       true,
	`<script><!--<script </s`:                      true,
	// Reconstructing the active formatting elements results in a <plaintext>
	// element that contains an <a> element.
	`<!doctype html><p><a><plaintext>b`:                       true,
	`<table><math><select><mi><select></table>`:               true,
	`<!doctype html><table><colgroup><plaintext></plaintext>`: true,
	`<!doctype html><svg><plaintext>a</plaintext>b`:           true,
	// Due to fostering, parsing the rendered output produces a different tree.
	`<math><mtext><table><mglyph><style><img>`: true,
}

func TestNodeConsistency(t *testing.T) {
	// inconsistentNode is a Node whose DataAtom and Data do not agree.
	inconsistentNode := &Node{
		Type:     ElementNode,
		DataAtom: atom.Frameset,
		Data:     "table",
	}
	if _, err := ParseFragment(strings.NewReader("<p>hello</p>"), inconsistentNode); err == nil {
		t.Errorf("got nil error, want non-nil")
	}
}

func TestParseFragmentWithNilContext(t *testing.T) {
	// This shouldn't panic.
	ParseFragment(strings.NewReader("<p>hello</p>"), nil)
}

func TestParseFragmentForeignContentTemplates(t *testing.T) {
	srcs := []string{
		"<math><html><template><mn><template></template></template>",
		"<math><math><head><mi><template>",
	}
	for _, src := range srcs {
		// The next line shouldn't infinite-loop.
		ParseFragment(strings.NewReader(src), nil)
	}
}

func BenchmarkParser(b *testing.B) {
	buf, err := os.ReadFile("testdata/go1.html")
	if err != nil {
		b.Fatalf("could not read testdata/go1.html: %v", err)
	}
	b.SetBytes(int64(len(buf)))
	runtime.GC()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Parse(bytes.NewBuffer(buf))
	}
}

func TestDepthLimit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		input   string
		succeed bool
	}{
		{"above depth limit", strings.Repeat("<dl>", 511), false},
		{"below depth limit", strings.Repeat("<dl>", 510), true},
		{"above depth limit, interspersed elements", strings.Repeat("<dl><img />", 511), false},
		{"closing tags", strings.Repeat("</dl>", 512), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tc.input))
			if tc.succeed && err != nil {
				t.Errorf("unexpected error: %v", err)
			} else if !tc.succeed && err == nil {
				t.Errorf("unexpected success")
			}
		})
	}
}

func TestIssue70179(t *testing.T) {
	_, err := Parse(strings.NewReader("<table><tbody><svg><td><desc><select></select></tbody>"))
	if err != nil {
		t.Fatalf("unexpected failure: %v", err)
	}
}

func TestNoahsArkClauseAttributeOrder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  int
	}{
		// Formatting elements with the same attributes in a different order are
		// identical, so the Noah's Ark clause keeps only three of them in the
		// list of active formatting elements.
		{"reordered attributes", `<p><b x=1 y=2><b y=2 x=1><b x=1 y=2><b y=2 x=1><p>X`, 3},
		// Formatting elements whose attribute values differ are not identical.
		{"different attribute values", `<p><b x=1 y=2><b y=3 x=1><b x=1 y=2><b y=3 x=1><p>X`, 4},
		// Formatting elements with a different number of attributes are not
		// identical.
		{"different attribute count", `<p><b x=1><b x=1 y=2><b x=1><b x=1 y=2><p>X`, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := Parse(strings.NewReader(tc.input))
			if err != nil {
				t.Fatalf("unexpected failure: %v", err)
			}
			var paragraphs []*Node
			var walk func(*Node)
			walk = func(n *Node) {
				if n.Type == ElementNode && n.DataAtom == atom.P {
					paragraphs = append(paragraphs, n)
				}
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
			}
			walk(doc)
			if len(paragraphs) != 2 {
				t.Fatalf("got %d <p> elements, want 2", len(paragraphs))
			}
			// The active formatting elements are reconstructed, nested, inside
			// the second paragraph.
			got := 0
			for n := paragraphs[1].FirstChild; n != nil && n.Type == ElementNode && n.DataAtom == atom.B; n = n.FirstChild {
				got++
			}
			if got != tc.want {
				t.Errorf("got %d reconstructed <b> elements, want %d", got, tc.want)
			}
		})
	}
}

// TestNoahsArkClauseManyAttributes checks that the Noah's Ark clause does not
// take time quadratic in the number of attributes of the formatting elements it
// compares. Comparing every attribute of one element against every attribute of
// the other let a small document with a few formatting elements carrying many
// attributes keep the parser busy for minutes.
func TestNoahsArkClauseManyAttributes(t *testing.T) {
	const (
		numElements = 16
		numAttrs    = 40000
		digits      = "0123456789abcdefghijklmnopqrstuvwxyz"
	)
	var attrs strings.Builder
	for i := 0; i < numAttrs; i++ {
		attrs.WriteByte(' ')
		attrs.WriteByte(digits[i/(36*36)])
		attrs.WriteByte(digits[i/36%36])
		attrs.WriteByte(digits[i%36])
	}
	var buf strings.Builder
	for i := 0; i < numElements; i++ {
		// The elements only differ in their last attribute, so a pairwise
		// comparison looks at almost every pair of attributes before it finds
		// the mismatch.
		fmt.Fprintf(&buf, "<b%s id=%d>", attrs.String(), i)
	}
	buf.WriteString("X")

	done := make(chan error, 1)
	go func() {
		_, err := Parse(strings.NewReader(buf.String()))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected failure: %v", err)
		}
	case <-time.After(time.Minute):
		t.Fatalf("parsing %d formatting elements with %d attributes each did not finish within a minute", numElements, numAttrs)
	}
}

func TestTextNodeCoalescing(t *testing.T) {
	const n = 10000
	for _, tc := range []struct {
		name  string
		input string
	}{
		{"body", strings.Repeat("a</x>", n)},
		{"foster", "<table>" + strings.Repeat("a</x>", n)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m1, m2 runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&m1)
			doc, err := Parse(strings.NewReader(tc.input))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			runtime.ReadMemStats(&m2)

			body := doc.FirstChild.LastChild
			text := body.FirstChild
			if text == nil || text.Type != TextNode {
				t.Fatalf("got first child %v, want TextNode", text)
			}
			if len(text.Data) != n {
				t.Errorf("got text length %d, want %d", len(text.Data), n)
			}

			// Coalescing n 1-byte text tokens should not require O(n^2) memory.
			if alloc := m2.TotalAlloc - m1.TotalAlloc; alloc > 5<<20 {
				t.Errorf("Parse allocated %d bytes, want <= %d", alloc, 5<<20)
			}
		})
	}
}

func TestTextNodeCoalescingFragment(t *testing.T) {
	const n = 10000
	context := &Node{
		Type:     ElementNode,
		DataAtom: atom.Div,
		Data:     atom.Div.String(),
	}
	var m1, m2 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m1)
	nodes, err := ParseFragment(strings.NewReader(strings.Repeat("a</x>", n)), context)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	runtime.ReadMemStats(&m2)

	if len(nodes) != 1 || nodes[0].Type != TextNode {
		t.Fatalf("got %d nodes, want a single TextNode", len(nodes))
	}
	if want := strings.Repeat("a", n); nodes[0].Data != want {
		t.Errorf("got text length %d, want %d", len(nodes[0].Data), n)
	}

	// Coalescing n 1-byte text tokens should not require O(n^2) memory.
	if alloc := m2.TotalAlloc - m1.TotalAlloc; alloc > 5<<20 {
		t.Errorf("ParseFragment allocated %d bytes, want <= %d", alloc, 5<<20)
	}
}
