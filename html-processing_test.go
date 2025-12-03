// This file is part of go-trafilatura, Go package for extracting readable
// content, comments and metadata from a web page. Source available in
// <https://github.com/markusmobius/go-trafilatura>.
//
// Copyright (C) 2021 Markus Mobius
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Code in this file is ported from <https://github.com/adbar/trafilatura>
// which available under Apache 2.0 license.

package trafilatura

import (
	"testing"

	"github.com/go-shiori/dom"
	"github.com/markusmobius/go-trafilatura/internal/etree"
	"github.com/stretchr/testify/assert"
	"golang.org/x/net/html"
)

func Test_processNode(t *testing.T) {
	var node *html.Node

	node = etree.FromString(`<div><p></p>tail</div>`)
	node = dom.QuerySelector(node, "p")
	node = processNode(node, nil, defaultOpts)
	assert.Equal(t, "tail", etree.Text(node))
	assert.Equal(t, "", etree.Tail(node))

	node = etree.FromString(`<ul><li></li>text in tail</ul>`)
	node = dom.QuerySelector(node, "li")
	node = processNode(node, nil, defaultOpts)
	assert.Equal(t, "text in tail", etree.Text(node))
	assert.Equal(t, "", etree.Tail(node))

	node = etree.FromString(`<p><br/>tail</p>`)
	node = dom.QuerySelector(node, "br")
	node = processNode(node, nil, defaultOpts)
	assert.Equal(t, "", etree.Text(node))
	assert.Equal(t, "tail", etree.Tail(node))

	node = etree.FromString(`<div><p>some text</p>tail</div>`)
	node = dom.QuerySelector(node, "p")
	node = processNode(node, nil, defaultOpts)
	assert.Equal(t, "some text", etree.Text(node))
	assert.Equal(t, "tail", etree.Tail(node))
}

func Test_docCleaning_IncludeLinksOnly_ContainerPreservation(t *testing.T) {
	htmlStr := `<div><ul><li><a href="/foo">foo</a></li><li><span><a href="/bar">bar</a></span></li><li><span>no link</span></li></ul><div><span><a href="/baz">baz</a></span></div><div><span>no link here</span></div></div>`
	doc := etree.FromString(htmlStr)
	opts := defaultOpts
	opts.IncludeLinksOnly = true

	docCleaning(doc, opts)

	// Only containers with <a> descendants should remain
	// There should be no <span> or <li> left that do not contain <a>
	remainingLinks := dom.GetElementsByTagName(doc, "a")
	assert.Equal(t, 3, len(remainingLinks))
	assert.Equal(t, "foo", dom.TextContent(remainingLinks[0]))
	assert.Equal(t, "bar", dom.TextContent(remainingLinks[1]))
	assert.Equal(t, "baz", dom.TextContent(remainingLinks[2]))

	// Check that the parent containers of each link are preserved
	fooParent := remainingLinks[0].Parent
	barParent := remainingLinks[1].Parent
	bazParent := remainingLinks[2].Parent
	assert.NotNil(t, fooParent)
	assert.NotNil(t, barParent)
	assert.NotNil(t, bazParent)
	// The <li> and <span> for foo and bar, <span> for baz
	assert.Contains(t, []string{"li", "span"}, dom.TagName(fooParent))
	assert.Contains(t, []string{"span", "li"}, dom.TagName(barParent))
	assert.Equal(t, "span", dom.TagName(bazParent))

	// Ensure containers without links are removed
	noLinkSpans := dom.QuerySelectorAll(doc, "span:not(:has(a))")
	assert.Equal(t, 0, len(noLinkSpans))
	noLinkLis := dom.QuerySelectorAll(doc, "li:not(:has(a))")
	assert.Equal(t, 0, len(noLinkLis))
	noLinkDivs := dom.QuerySelectorAll(doc, "div:not(:has(a))")
	assert.Equal(t, 0, len(noLinkDivs))
}
