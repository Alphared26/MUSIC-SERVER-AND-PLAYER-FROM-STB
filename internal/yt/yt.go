package yt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Track struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Artist    string `json:"artist"`
	Album     string `json:"album"`
	Duration  string `json:"duration"`
	Thumbnail string `json:"thumbnail"`
}

type Service struct {
	mu     sync.Mutex
	cache  map[string]cachedURL
	client *http.Client
}

type cachedURL struct {
	url string
	exp time.Time
}

func NewService() *Service {
	return &Service{
		cache: make(map[string]cachedURL),
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Search queries YouTube Music InnerTube API (pure Go, no external process).
func (s *Service) Search(ctx context.Context, query string) ([]Track, error) {
	if strings.TrimSpace(query) == "" {
		return []Track{}, nil
	}

	payload := map[string]any{
		"context": map[string]any{
			"client": map[string]any{
				"clientName":    "WEB_REMIX",
				"clientVersion": "1.20240101.01.00",
				"hl":            "en",
				"gl":            "US",
			},
		},
		"query":  query,
		"params": "EgWKAQIIAWoQEAMQBBAJEAoQBRAREBAQFQ==", // Song filter
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://music.youtube.com/youtubei/v1/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search failed with status %d", resp.StatusCode)
	}

	var root map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&root); err != nil {
		return nil, err
	}

	return parseSearchResults(root), nil
}

func parseSearchResults(root map[string]any) []Track {
	var tracks []Track

	contents, _ := root["contents"].(map[string]any)
	tabbed, _ := contents["tabbedSearchResultsRenderer"].(map[string]any)
	tabs, _ := tabbed["tabs"].([]any)
	if len(tabs) == 0 {
		return tracks
	}
	tab0, _ := tabs[0].(map[string]any)
	tabRen, _ := tab0["tabRenderer"].(map[string]any)
	content, _ := tabRen["content"].(map[string]any)
	secList, _ := content["sectionListRenderer"].(map[string]any)
	secContents, _ := secList["contents"].([]any)

	for _, sec := range secContents {
		secMap, _ := sec.(map[string]any)
		shelf, _ := secMap["musicShelfRenderer"].(map[string]any)
		shelfContents, _ := shelf["contents"].([]any)

		for _, item := range shelfContents {
			itemMap, _ := item.(map[string]any)
			flex, _ := itemMap["musicResponsiveListItemRenderer"].(map[string]any)
			if flex == nil {
				continue
			}

			// Video ID
			overlay, _ := flex["overlay"].(map[string]any)
			thumbOverlay, _ := overlay["musicItemThumbnailOverlayRenderer"].(map[string]any)
			contentO, _ := thumbOverlay["content"].(map[string]any)
			playBtn, _ := contentO["musicPlayButtonRenderer"].(map[string]any)
			nav, _ := playBtn["playNavigationEndpoint"].(map[string]any)
			watch, _ := nav["watchEndpoint"].(map[string]any)
			vid, _ := watch["videoId"].(string)

			// Flex columns
			cols, _ := flex["flexColumns"].([]any)
			if len(cols) == 0 || vid == "" {
				continue
			}

			// Title
			col0, _ := cols[0].(map[string]any)
			flexCol0, _ := col0["musicResponsiveListItemFlexColumnRenderer"].(map[string]any)
			text0, _ := flexCol0["text"].(map[string]any)
			runs0, _ := text0["runs"].([]any)
			title := ""
			if len(runs0) > 0 {
				r0, _ := runs0[0].(map[string]any)
				title, _ = r0["text"].(string)
			}

			// Artist, Album, Duration
			artist := "Unknown Artist"
			album := ""
			duration := ""
			if len(cols) > 1 {
				col1, _ := cols[1].(map[string]any)
				flexCol1, _ := col1["musicResponsiveListItemFlexColumnRenderer"].(map[string]any)
				text1, _ := flexCol1["text"].(map[string]any)
				runs1, _ := text1["runs"].([]any)

				var textParts []string
				for _, r := range runs1 {
					rMap, _ := r.(map[string]any)
					if t, ok := rMap["text"].(string); ok && t != " • " && t != " & " {
						textParts = append(textParts, t)
					}
				}
				if len(textParts) > 0 {
					artist = textParts[0]
				}
				if len(textParts) == 2 {
					duration = textParts[1]
				} else if len(textParts) >= 3 {
					album = textParts[1]
					duration = textParts[len(textParts)-1]
				}
			}

			// Thumbnail
			thumbObj, _ := flex["thumbnail"].(map[string]any)
			thumbRen, _ := thumbObj["musicThumbnailRenderer"].(map[string]any)
			thumbData, _ := thumbRen["thumbnail"].(map[string]any)
			thumbs, _ := thumbData["thumbnails"].([]any)
			thumbURL := ""
			if len(thumbs) > 0 {
				tLast, _ := thumbs[len(thumbs)-1].(map[string]any)
				thumbURL, _ = tLast["url"].(string)
			}

			tracks = append(tracks, Track{
				ID:        vid,
				Title:     title,
				Artist:    artist,
				Album:     album,
				Duration:  duration,
				Thumbnail: thumbURL,
			})
		}
	}
	return tracks
}

// ResolveStream returns the direct streaming URL for a YouTube video ID.
// Uses an in-memory cache to ensure yt-dlp is called at most once per track.
func (s *Service) ResolveStream(ctx context.Context, id string) (string, error) {
	if id == "" {
		return "", errors.New("empty video id")
	}

	s.mu.Lock()
	if c, ok := s.cache[id]; ok && time.Now().Before(c.exp) {
		s.mu.Unlock()
		return c.url, nil
	}
	s.mu.Unlock()

	// Locate yt-dlp
	bin := "/usr/local/bin/yt-dlp"
	if _, err := os.Stat(bin); err != nil {
		var errLook error
		bin, errLook = exec.LookPath("yt-dlp")
		if errLook != nil {
			return "", fmt.Errorf("yt-dlp binary not found: %w", errLook)
		}
	}

	cmdCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, bin, "-g", "-f", "ba[ext=m4a]/ba[ext=mp3]/bestaudio/ba", "https://www.youtube.com/watch?v="+id)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve stream url failed: %w", err)
	}

	url := strings.TrimSpace(string(out))
	if url == "" {
		return "", errors.New("empty stream url returned")
	}

	s.mu.Lock()
	s.cache[id] = cachedURL{
		url: url,
		exp: time.Now().Add(3 * time.Hour),
	}
	s.mu.Unlock()

	return url, nil
}

// Download downloads the audio track directly into outputDir.
func (s *Service) Download(ctx context.Context, id, outputDir string) error {
	if id == "" {
		return errors.New("empty video id")
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	bin := "/usr/local/bin/yt-dlp"
	if _, err := os.Stat(bin); err != nil {
		var errLook error
		bin, errLook = exec.LookPath("yt-dlp")
		if errLook != nil {
			return fmt.Errorf("yt-dlp binary not found: %w", errLook)
		}
	}

	outTemplate := filepath.Join(outputDir, "%(artist,creator,uploader)s - %(title)s.%(ext)s")
	cmdCtx, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, bin,
		"-f", "bestaudio",
		"-x",
		"--audio-format", "m4a",
		"--embed-metadata",
		"--embed-thumbnail",
		"-o", outTemplate,
		"https://www.youtube.com/watch?v="+id,
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("download failed (%w): %s", err, string(out))
	}
	return nil
}
