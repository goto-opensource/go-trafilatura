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

package main

import (
	"fmt"
	"net/http"
	nurl "net/url"
	"time"

	"github.com/go-shiori/dom"
	"github.com/markusmobius/go-trafilatura"
	"github.com/spf13/cobra"
	"golang.org/x/net/publicsuffix"
	"golang.org/x/sync/semaphore"
)

func crawlCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "crawl [flags] [source]",
		Short: "crawl a fixed number of pages within a website starting from the given URL",
		Long: "Crawl a fixed number of pages within a website starting from the given URL.\n" +
			"TODO: complete desct",
		Args: cobra.ExactArgs(1),
		Run:  crawlCmdHandler,
	}

	flags := cmd.Flags()
	flags.Int("parallel", 10, "number of concurrent download at a time (default 10)")
	flags.Int("delay", 0, "delay between each url download in seconds (default 0)")

	flags.Int("max_urls", 0, "maximum number of URLs to download (default 0)")
	flags.Int("max_depth", 2, "maximum recursion depth (default 2)")
	flags.Bool("same_domain", true, "restrict recursion to same domain")

	return cmd
}

func crawlCmdHandler(cmd *cobra.Command, args []string) {
	// Parse arguments
	flags := cmd.Flags()
	delay, _ := flags.GetInt("delay")
	nThread, _ := flags.GetInt("parallel")
	nURL, _ := flags.GetInt("max_urls")
	nDepth, _ := flags.GetInt("max_depth")
	userAgent, _ := cmd.Flags().GetString("user-agent")
	sameDomain, _ := flags.GetBool("same_domain")

	log.Info().Int("nURL", nURL).Int("nDepth", nDepth).Int("nThread", nThread).Msgf("Crawling URL")

	opts := createExtractorOptions(cmd)
	opts.IncludeLinksOnly = true
	opts.Focus = trafilatura.FavorPrecision
	opts.EnableFallback = false

	urls, err := (&crawler{
		userAgent:      userAgent,
		httpClient:     createHttpClient(cmd),
		extractOptions: opts,
		semaphore:      semaphore.NewWeighted(int64(nThread)),
		delay:          time.Duration(delay) * time.Second,
		cancelOnError:  false,
		sameDomain:     sameDomain,
		maxURLs:        nURL,
		maxDepth:       nDepth,
	}).extractURLs(args[0])

	if err != nil {
		log.Fatal().Msgf("process failed: %v", err)
	}
	log.Info().Msgf("Crawled %d URLs", len(urls))
}

type crawler struct {
	extractOptions trafilatura.Options
	semaphore      *semaphore.Weighted
	httpClient     *http.Client
	userAgent      string
	delay          time.Duration
	cancelOnError  bool
	sameDomain     bool
	maxURLs        int
	maxDepth       int
	urls           []string
}

func extractLinksFromURL(httpClient *http.Client, userAgent string, source string, opts trafilatura.Options) ([]string, error) {
	if !isValidURL(source) {
		return nil, fmt.Errorf("invalid URL: %s", source)
	}
	parsedURL, err := nurl.ParseRequestURI(source)
	if err != nil {
		return nil, err
	}
	result, err := processURL(httpClient, userAgent, parsedURL, opts)
	if err != nil {
		return nil, err
	}
	log.Info().Str("url", source).Int("size", len(result.ContentText)).Msgf("Found URL")
	links := dom.QuerySelectorAll(result.ContentNode, "a[href]")
	urls := make([]string, 0, len(links))
	for _, link := range links {
		href := dom.GetAttribute(link, "href")
		if href != "" {
			urls = append(urls, href)
		}
	}
	return urls, nil
}

func (c *crawler) extractURLs(source string) ([]string, error) {
	maxURLs := c.maxURLs
	maxDepth := c.maxDepth

	visited := make(map[string]bool)
	var result []string
	queue := []struct {
		url   string
		depth int
	}{{source, 0}}

	sourceURL, err := nurl.Parse(source)
	if err != nil {
		return nil, err
	}
	sourceTLD, err := publicsuffix.EffectiveTLDPlusOne(sourceURL.Hostname())
	if err != nil {
		return nil, err
	}

	for len(queue) > 0 && (maxURLs == 0 || len(result) < maxURLs) {
		item := queue[0]
		queue = queue[1:]

		if visited[item.url] || (maxDepth > 0 && item.depth > maxDepth) {
			continue
		}
		visited[item.url] = true
		result = append(result, item.url)

		children, err := extractLinksFromURL(c.httpClient, c.userAgent, item.url, c.extractOptions)
		if err != nil {
			log.Warn().Msgf("failed to extract links: %v", err)
			continue
		}

		for _, child := range children {
			if visited[child] {
				continue
			}
			childURL, err := nurl.Parse(child)
			if err != nil {
				continue
			}
			childTLD, err := publicsuffix.EffectiveTLDPlusOne(childURL.Hostname())
			if err != nil {
				continue
			}
			if childTLD != sourceTLD {
				log.Info().Msgf("Skipping URL %s because it's a different TLD", child)
				continue
			}
			queue = append(queue, struct {
				url   string
				depth int
			}{child, item.depth + 1})
		}
	}

	return result, nil
}
