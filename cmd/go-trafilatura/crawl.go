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
	"net/http"
	nurl "net/url"
	"time"

	"github.com/markusmobius/go-trafilatura"
	"github.com/spf13/cobra"
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

	log.Info().Int("nURL", nURL).Int("nDepth", nDepth).Int("nThread", nThread).Msgf("Crawling URL")

	opts := createExtractorOptions(cmd)
	opts.IncludeLinksOnly = true
	opts.Focus = trafilatura.FavorPrecision
	opts.EnableFallback = false

	err := (&crawler{
		userAgent:      userAgent,
		httpClient:     createHttpClient(cmd),
		extractOptions: createExtractorOptions(cmd),
		semaphore:      semaphore.NewWeighted(int64(nThread)),
		delay:          time.Duration(delay) * time.Second,
		cancelOnError:  false,
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
}

// extractURLs based on batchDownloader
func (c *crawler) extractURLs(ctx context.Context, source string) error {
	g, ctx := errgroup.WithContext(context.Background())

	var err error
	var result *trafilatura.ExtractResult

	if isValidURL(source) {
		parsedURL, _ := nurl.ParseRequestURI(source)
		result, err = processURL(c.httpClient, c.userAgent, parsedURL, c.extractOptions)
		if err != nil {
			return err
		}
		log.Info().Str("url", source).Int("size", len(result.ContentText)).Msgf("Found URL")
	}
	urls := make([]string, 0)
	urls = append(urls, source)

	for _, url := range urls {
		parsedURL, _ := nurl.ParseRequestURI(source)

		g.Go(func() error {
			// Acquire semaphore to limit concurrent download
			err := c.semaphore.Acquire(ctx, 1)
			if err != nil {
				return nil
			}

			// Process URL
			result, err := processURL(c.httpClient, c.userAgent, parsedURL, c.extractOptions)
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
