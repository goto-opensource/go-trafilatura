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

	"github.com/go-shiori/dom"
	"github.com/markusmobius/go-trafilatura"
	"github.com/spf13/cobra"
	"golang.org/x/net/publicsuffix"
	"golang.org/x/sync/errgroup"
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
	flags.Int("max_depth", 0, "maximum recursion depth (default 0)")
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

	err := (&crawler{
		userAgent:      userAgent,
		httpClient:     createHttpClient(cmd),
		extractOptions: opts,
		semaphore:      semaphore.NewWeighted(int64(nThread)),
		delay:          time.Duration(delay) * time.Second,
		cancelOnError:  false,
		sameDomain:     sameDomain,
	}).extractURLs(context.Background(), args[0])

	if err != nil {
		log.Fatal().Msgf("process failed: %v", err)
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

func (c *crawler) extractURLs(ctx context.Context, source string) ([]string, error) {
	g, ctx := errgroup.WithContext(context.Background())

	children, err := extractLinksFromURL(c.httpClient, c.userAgent, source, c.extractOptions)
	if err != nil {
		log.Error().Msgf("failed to extract links: %v", err)
	}
	// process urls
	log.Info().Str("url", source).Int("size", len(urls)).Msgf("Found URLs")

	// recursively extract URLS from the same tldr only
	for _, url := range children {
		// Only allow following same domain
		if c.sameDomain {
			sourceURL, _ := nurl.Parse(source)
			urlToCheck, _ := nurl.Parse(url)
			sourceTLD, _ := publicsuffix.EffectiveTLDPlusOne(sourceURL.Hostname())
			urlTLD, _ := publicsuffix.EffectiveTLDPlusOne(urlToCheck.Hostname())

			if sourceTLD != urlTLD {
				log.Info().Msgf("Skipping URL %s because it's a different TLD", url)
				continue
			}
		}

		g.Go(func() error {
			// Acquire semaphore to limit concurrent download
			err := c.semaphore.Acquire(ctx, 1)
			if err != nil {
				return nil
			}
			next_children, err := extractLinksFromURL(c.httpClient, c.userAgent, source, c.extractOptions)

			c.semaphore.Release(1)
			if err != nil {
				if c.cancelOnError {
					return err
				}
				log.Warn().Msgf("failed to process %s: %v", url, err)
				return nil
			}
			log.Info().Str("url", source).Int("size", len(result.ContentText)).Msgf("Found URL")

			// TODO

			// Add delay (to prevent too many request to target server)
			time.Sleep(c.delay)
			return nil
		})
	}

	return g.Wait()
}
