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
	"context"
	"fmt"
	"net/http"
	nurl "net/url"
	"time"

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
			"Recursively follows links up to a specified depth, downloading pages in parallel.\n" +
			"Options allow you to set the maximum number of URLs, recursion depth, delay between requests, and restrict crawling to the same domain.\n" +
			"Useful for collecting URLs or content from a site for further processing.",
		Args: cobra.ExactArgs(1),
		Run:  crawlCmdHandler,
	}

	flags := cmd.Flags()
	flags.Int("parallel", 10, "number of concurrent download at a time (default 10)")
	flags.Duration("delay", time.Millisecond*250, "delay between each url download (default 250ms)")

	flags.Int("max_urls", 0, "maximum number of URLs to download (default 0)")
	flags.Int("max_depth", 2, "maximum recursion depth (default 2)")
	flags.Bool("same_domain", true, "restrict recursion to same domain")

	return cmd
}

func crawlCmdHandler(cmd *cobra.Command, args []string) {
	// Parse arguments
	flags := cmd.Flags()
	delay, _ := flags.GetDuration("delay")
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
		delay:          delay,
		cancelOnError:  false,
		sameDomain:     sameDomain,
		maxURLs:        nURL,
		maxDepth:       nDepth,
	}).extractURLs(context.Background(), args[0])

	if err != nil {
		log.Fatal().Msgf("process failed: %v", err)
	}
	log.Info().Msgf("Crawled %d URLs", len(urls))
	for _, url := range urls {
		log.Info().Msgf("%s", url)
	}
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
	log.Info().Str("url", source).Int("size", len(result.URLs)).Msgf("Found URL")
	return result.URLs, nil
}

func (c *crawler) extractURLs(context context.Context, source string) ([]string, error) {
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
		// Group all items at the current depth
		currentDepth := queue[0].depth
		var currentLevel []struct {
			url   string
			depth int
		}
		for len(queue) > 0 && queue[0].depth == currentDepth {
			currentLevel = append(currentLevel, queue[0])
			queue = queue[1:]
		}

		// Channel to collect children from all goroutines
		childrenCh := make(chan []string, len(currentLevel))
		goroutinesStarted := 0

		// Process all URLs at this depth in parallel
		for _, item := range currentLevel {
			if visited[item.url] || (maxDepth > 0 && item.depth > maxDepth) {
				continue
			}
			visited[item.url] = true
			result = append(result, item.url)

			// Acquire semaphore for parallelism
			if err := c.semaphore.Acquire(context, 1); err != nil {
				childrenCh <- nil
				continue
			}
			goroutinesStarted++

			go func(item struct {
				url   string
				depth int
			}) {
				defer c.semaphore.Release(1)
				children, err := extractLinksFromURL(c.httpClient, c.userAgent, item.url, c.extractOptions)
				if err != nil {
					log.Warn().Msgf("failed to extract links: %v", err)
					childrenCh <- nil
					return
				}
				if c.delay > 0 {
					time.Sleep(c.delay)
				}
				childrenCh <- children
			}(item)
		}

		// Collect all children from this depth
		var allChildren []string
		for i := 0; i < goroutinesStarted; i++ {
			children := <-childrenCh
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
					log.Debug().Msgf("Skipping URL %s because it's a different TLD", child)
					continue
				}
				allChildren = append(allChildren, child)
			}
		}

		// Enqueue deduplicated children for next depth
		for _, child := range allChildren {
			if !visited[child] {
				queue = append(queue, struct {
					url   string
					depth int
				}{child, currentDepth + 1})
			}
		}
	}

	return result, nil
}
