package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/genai"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	http.HandleFunc("/start", startHandler)
	http.HandleFunc("/clip", clipHandler)
	http.HandleFunc("/status", statusHandler)
	http.HandleFunc("/guess", guessHandler)
	http.HandleFunc("/reveal", revealHandler)
	http.HandleFunc("/refreshCache", refreshCacheHandler)

	fmt.Println("Songs AI game server listening on :8080")
	return http.ListenAndServe(":8080", nil)
}

type Round struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	Artist     string    `json:"artist"`
	YouTube    string    `json:"youtube"`
	ClipPath   string    `json:"-"`
	Ready      bool      `json:"ready"`
	Error      string    `json:"-"`
	ClipLength int       `json:"-"`
	CreatedAt  time.Time `json:"created_at"`
}

var (
	rounds         = map[string]*Round{}
	roundsMu       sync.Mutex
	usedMu         sync.Mutex
	usedVideos     = map[string]struct{}{}
	rng            = rand.New(rand.NewSource(time.Now().UnixNano()))
	bannedKeywords = []string{"mix", "compilation", "medley", "playlist", "full album", "full song", "continuous", "best of", "mega mix", "mashup", "various artists", "compilations", "album", "album version", "greatest hits", "popular songs", "top hits"}

	// Song cache from Gemini
	songCacheMu   sync.Mutex
	songCache     = []struct{ Title, Artist string }{}
	songCacheIdx  = 0
	songCacheLang = ""
)

func startHandler(w http.ResponseWriter, r *http.Request) {
	setCORS(w)
	if r.Method == http.MethodOptions {
		return
	}
	lang := r.URL.Query().Get("lang")
	if lang == "" {
		http.Error(w, "missing lang parameter, e.g. ?lang=english", http.StatusBadRequest)
		return
	}

	clipLength := 30
	if cl := r.URL.Query().Get("clipLength"); cl != "" {
		if parsed, err := strconv.Atoi(cl); err == nil && parsed > 0 && parsed <= 300 {
			clipLength = parsed
		}
	}

	title, artist, yt, err := searchYouTubeForSong(lang)
	if err != nil {
		http.Error(w, fmt.Sprintf("search error: %v", err), http.StatusInternalServerError)
		return
	}

	id := randomID(8)
	rinfo := &Round{ID: id, Title: title, Artist: artist, YouTube: yt, Ready: false, ClipLength: clipLength, CreatedAt: time.Now()}
	roundsMu.Lock()
	rounds[id] = rinfo
	roundsMu.Unlock()

	// download clip in background so we return immediately
	go func(rid, youtube string, clipLen int) {
		path, derr := download10sClip(youtube, clipLen)
		roundsMu.Lock()
		defer roundsMu.Unlock()
		rr := rounds[rid]
		if rr == nil {
			return
		}
		if derr != nil {
			rr.Error = derr.Error()
			rr.Ready = false
		} else {
			rr.ClipPath = path
			rr.Ready = true
		}
	}(id, yt, clipLength)

	resp := map[string]string{"id": id, "clip_url": fmt.Sprintf("/clip?id=%s", url.QueryEscape(id))}
	writeJSON(w, resp)
}

func clipHandler(w http.ResponseWriter, r *http.Request) {
	setCORS(w)
	if r.Method == http.MethodOptions {
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	roundsMu.Lock()
	ri := rounds[id]
	roundsMu.Unlock()
	if ri == nil {
		http.Error(w, "round not found", http.StatusNotFound)
		return
	}
	if !ri.Ready {
		if ri.Error != "" {
			http.Error(w, fmt.Sprintf("clip error: %s", ri.Error), http.StatusInternalServerError)
		} else {
			http.Error(w, "clip not ready yet", http.StatusServiceUnavailable)
		}
		return
	}
	f, err := os.Open(ri.ClipPath)
	if err != nil {
		http.Error(w, "clip open error", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "audio/mpeg")
	io.Copy(w, f)
}

func guessHandler(w http.ResponseWriter, r *http.Request) {
	setCORS(w)
	if r.Method == http.MethodOptions {
		return
	}
	var req struct {
		ID    string `json:"id"`
		Guess string `json:"guess"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	roundsMu.Lock()
	ri := rounds[req.ID]
	roundsMu.Unlock()
	if ri == nil {
		http.Error(w, "round not found", http.StatusNotFound)
		return
	}
	// forgiving matching: user guess appears in title/artist OR title/artist appears in guess
	guessLow := strings.ToLower(strings.TrimSpace(req.Guess))
	titleLow := strings.ToLower(strings.TrimSpace(ri.Title))
	artistLow := strings.ToLower(strings.TrimSpace(ri.Artist))
	ok := false
	if guessLow != "" {
		if strings.Contains(titleLow, guessLow) || strings.Contains(artistLow, guessLow) {
			ok = true
		}
		if strings.Contains(guessLow, titleLow) || strings.Contains(guessLow, artistLow) {
			ok = true
		}
	}
	writeJSON(w, map[string]interface{}{"correct": ok})
}

func statusHandler(w http.ResponseWriter, r *http.Request) {
	setCORS(w)
	if r.Method == http.MethodOptions {
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	roundsMu.Lock()
	ri := rounds[id]
	roundsMu.Unlock()
	if ri == nil {
		http.Error(w, "round not found", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]interface{}{"ready": ri.Ready, "error": ri.Error})
}

func revealHandler(w http.ResponseWriter, r *http.Request) {
	setCORS(w)
	if r.Method == http.MethodOptions {
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	roundsMu.Lock()
	ri := rounds[id]
	roundsMu.Unlock()
	if ri == nil {
		http.Error(w, "round not found", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]string{"title": ri.Title, "artist": ri.Artist, "youtube": ri.YouTube})
}

func refreshCacheHandler(w http.ResponseWriter, r *http.Request) {
	setCORS(w)
	if r.Method == http.MethodOptions {
		return
	}
	lang := r.URL.Query().Get("lang")
	if lang == "" {
		http.Error(w, "missing lang parameter", http.StatusBadRequest)
		return
	}

	log.Printf("Refreshing song cache for language: %s", lang)

	// Fetch new songs from Gemini
	songs, err := craftSongList(lang)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to fetch songs: %v", err), http.StatusInternalServerError)
		return
	}

	// Update the cache
	songCacheMu.Lock()
	songCache = songs
	songCacheIdx = 0
	songCacheLang = lang
	log.Printf("Cache refreshed with %d songs for language: %s", len(songCache), lang)
	songCacheMu.Unlock()

	writeJSON(w, map[string]interface{}{"status": "cache refreshed", "songs_loaded": len(songs)})
}

func searchYouTubeForSong(lang string) (title, artist, youtubeURL string, err error) {
	serpKey := os.Getenv("SERPAPI_API_KEY")
	// try to craft a better query via Gemini if available
	qstr, _ := craftSearchQuery(lang)
	if qstr != "" {
		log.Printf("crafted search query: %s", qstr)
	}
	if qstr == "" {
		qstr = fmt.Sprintf("popular songs in %s YouTube from the last 2 years", lang)
	}
	gemKey := os.Getenv("GEMINI_API_KEY")
	log.Printf("GEMINI_API_KEY present: %v", gemKey != "")

	// Check if we need to refresh the song cache
	songCacheMu.Lock()
	needsRefresh := gemKey != "" && (len(songCache) == 0 || songCacheLang != lang)
	songCacheMu.Unlock()

	if needsRefresh {
		log.Printf("Refreshing song cache from Gemini for language: %s", lang)
		if songs, err := craftSongList(lang); err == nil && len(songs) > 0 {
			songCacheMu.Lock()
			songCache = songs
			songCacheIdx = 0
			songCacheLang = lang
			log.Printf("Loaded %d songs into cache", len(songCache))
			songCacheMu.Unlock()
		} else {
			log.Printf("Failed to fetch songs from Gemini: %v", err)
		}
	}

	// Try to use songs from cache
	songCacheMu.Lock()
	if len(songCache) > 0 && songCacheLang == lang {
		// Try songs starting from current index
		startIdx := songCacheIdx
		for i := 0; i < len(songCache); i++ {
			idx := (startIdx + i) % len(songCache)
			s := songCache[idx]
			songCacheIdx = (idx + 1) % len(songCache)
			songCacheMu.Unlock()

			sq := s.Title
			if s.Artist != "" {
				sq = fmt.Sprintf("%s %s", s.Title, s.Artist)
			}
			log.Printf("Searching YouTube for cached song: %s", sq)

			// Use yt-dlp to search for this song
			cmd := exec.Command("yt-dlp", "--no-warnings", "-J", fmt.Sprintf("ytsearch1:%s", sq))
			out, err := cmd.CombinedOutput()
			if err != nil {
				log.Printf("yt-dlp search error for %s: %v", sq, err)
				songCacheMu.Lock()
				continue
			}

			info, err := parseJSONWithRecovery(out)
			if err != nil {
				log.Printf("JSON parse error for %s: %v", sq, err)
				songCacheMu.Lock()
				continue
			}

			// Extract video info
			var videoURL string
			var duration int

			// Try entries array first
			if entries, ok := info["entries"].([]interface{}); ok && len(entries) > 0 {
				if e0, ok := entries[0].(map[string]interface{}); ok {
					if uu, ok := e0["webpage_url"].(string); ok {
						videoURL = uu
					}
					if d, ok := e0["duration"].(float64); ok {
						duration = int(d)
					}
				}
			}

			// Fallback to top-level fields
			if videoURL == "" {
				if uu, ok := info["webpage_url"].(string); ok {
					videoURL = uu
				}
				if d, ok := info["duration"].(float64); ok {
					duration = int(d)
				}
			}

			// Validate the result
			if videoURL == "" {
				log.Printf("No video URL found for %s", sq)
				songCacheMu.Lock()
				continue
			}

			// Check duration - skip if too long (> 8 minutes = 480s) or too short (< 20s)
			if duration > 0 && (duration < 20 || duration > 480) {
				log.Printf("Skipping %s - duration %d seconds is out of range", sq, duration)
				songCacheMu.Lock()
				continue
			}

			// Check if banned and not already used
			if isBanned(s.Title, bannedKeywords) {
				log.Printf("Skipping %s - title contains banned keywords", sq)
				songCacheMu.Lock()
				continue
			}

			if id := extractYouTubeID(videoURL); id != "" && !isUsed(id) {
				markUsed(id)
				log.Printf("Using cached song: %s by %s (cache position %d/%d)", s.Title, s.Artist, idx+1, len(songCache))
				return s.Title, s.Artist, videoURL, nil
			}

			songCacheMu.Lock()
		}
		songCacheMu.Unlock()
		log.Printf("No usable songs in cache, will fall back to search")
	} else {
		songCacheMu.Unlock()
		log.Printf("Song cache is empty or language mismatch")
	}

	if serpKey != "" {
		q := url.QueryEscape(qstr)
		api := fmt.Sprintf("https://serpapi.com/search.json?q=%s&engine=google&api_key=%s", q, serpKey)
		resp, err := http.Get(api)
		if err != nil {
			return "", "", "", err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		log.Printf("SerpAPI response (truncated): %s", short(string(body), 800))
		var data map[string]interface{}
		if err = json.Unmarshal(body, &data); err != nil {
			return "", "", "", err
		}

		// collect candidates from organic_results and video_results (skip banned titles and already-used videos)
		type cand struct{ link, title string }
		var cands []cand
		if org, ok := data["organic_results"].([]interface{}); ok {
			for _, it := range org {
				m, _ := it.(map[string]interface{})
				titleField := ""
				if t, ok := m["title"].(string); ok {
					titleField = t
				}
				if link, ok := m["link"].(string); ok && strings.Contains(link, "youtube.com/watch") {
					if isBanned(titleField, bannedKeywords) {
						continue
					}
					// attempt to check duration and skip videos longer than 8 minutes (480s)
					if dur, derr := getYouTubeDurationSeconds(link); derr == nil && dur > 0 && dur > 480 {
						continue
					}
					if id := extractYouTubeID(link); id != "" && !isUsed(id) {
						cands = append(cands, cand{link: link, title: titleField})
					}
				}
			}
		}
		if vids, ok := data["video_results"].([]interface{}); ok {
			for _, it := range vids {
				m, _ := it.(map[string]interface{})
				titleField := ""
				if t, ok := m["title"].(string); ok {
					titleField = t
				}
				if link, ok := m["link"].(string); ok && strings.Contains(link, "youtube.com/watch") {
					if isBanned(titleField, bannedKeywords) {
						continue
					}
					// attempt to check duration and skip videos longer than 8 minutes (480s)
					if dur, derr := getYouTubeDurationSeconds(link); derr == nil && dur > 0 && dur > 480 {
						continue
					}
					if id := extractYouTubeID(link); id != "" && !isUsed(id) {
						cands = append(cands, cand{link: link, title: titleField})
					}
				}
			}
		}
		if len(cands) > 0 {
			idx := rng.Intn(len(cands))
			youtubeURL = cands[idx].link
			title = cands[idx].title
			if id := extractYouTubeID(youtubeURL); id != "" {
				markUsed(id)
			}
		}
		if youtubeURL == "" {
			err := fmt.Errorf("no youtube link found")
			return "", "", "", err
		}

		// fetch oembed for title/author
		oembed := fmt.Sprintf("https://www.youtube.com/oembed?url=%s&format=json", url.QueryEscape(youtubeURL))
		r2, err := http.Get(oembed)
		if err != nil {
			return "", "", "", err
		}
		defer r2.Body.Close()
		b2, _ := io.ReadAll(r2.Body)
		var o map[string]interface{}
		if err = json.Unmarshal(b2, &o); err == nil {
			if t, ok := o["title"].(string); ok {
				title = t
			}
			if a, ok := o["author_name"].(string); ok {
				artist = a
			}
		}
		return title, artist, youtubeURL, nil
	}

	// If SerpAPI not available, use yt-dlp to search YouTube directly
	// Requires yt-dlp on PATH. Request multiple results and pick a single-song candidate.
	cmd := exec.Command("yt-dlp", "--no-warnings", "-J", fmt.Sprintf("ytsearch5:%s", qstr))
	out, err := cmd.CombinedOutput()
	log.Printf("yt-dlp search output (truncated): %s", short(string(out), 2000))
	if err != nil {
		log.Printf("yt-dlp search error: %v", err)
		// fallback sample
		return "Sample Song", "Sample Artist", "https://www.youtube.com/watch?v=dQw4w9WgXcQ", nil
	}
	info, err := parseJSONWithRecovery(out)
	if err != nil {
		log.Printf("yt-dlp search parse error: %v", err)
		return "Sample Song", "Sample Artist", "https://www.youtube.com/watch?v=dQw4w9WgXcQ", nil
	}
	// prefer entries array
	if entries, ok := info["entries"].([]interface{}); ok {
		type cand struct {
			link, title, uploader string
			dur                   int
		}
		var cands []cand
		for _, e := range entries {
			m, _ := e.(map[string]interface{})
			tstr := ""
			if t, ok := m["title"].(string); ok {
				tstr = t
			}
			dur := 0
			if d, ok := m["duration"].(float64); ok {
				dur = int(d)
			}
			if isBanned(tstr, bannedKeywords) {
				continue
			}
			if dur > 0 && (dur < 20 || dur > 480) {
				continue
			}
			u := ""
			if uu, ok := m["webpage_url"].(string); ok {
				u = uu
			}
			uploader := ""
			if up, ok := m["uploader"].(string); ok {
				uploader = up
			}
			if id := extractYouTubeID(u); id != "" && !isUsed(id) {
				cands = append(cands, cand{link: u, title: tstr, uploader: uploader, dur: dur})
			}
		}
		if len(cands) > 0 {
			idx := rng.Intn(len(cands))
			youtubeURL = cands[idx].link
			title = cands[idx].title
			artist = cands[idx].uploader
			if id := extractYouTubeID(youtubeURL); id != "" {
				markUsed(id)
			}
			return title, artist, youtubeURL, nil
		}
	}
	// fallback single fields
	if u, ok := info["webpage_url"].(string); ok && u != "" {
		youtubeURL = u
		if t, ok := info["title"].(string); ok {
			title = t
		}
		if a, ok := info["uploader"].(string); ok {
			artist = a
		}
	}
	if youtubeURL == "" {
		return "Sample Song", "Sample Artist", "https://www.youtube.com/watch?v=dQw4w9WgXcQ", nil
	}
	return title, artist, youtubeURL, nil
}

// craftSearchQuery uses the Google GenAI SDK to produce a concise search query
// for finding popular songs in the requested language.
func craftSearchQuery(lang string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey: os.Getenv("GEMINI_API_KEY"),
	})
	if err != nil {
		return "", err
	}

	prompt := fmt.Sprintf("Produce a short web search query (one line) to find popular YouTube songs in the %s language. Prefer concise keywords only, suitable for use in a search engine (no extra explanation). Bias results toward recent releases (last 2 years).", lang)

	resp, err := client.Models.GenerateContent(ctx, "gemini-2.5-flash", genai.Text(prompt), nil)
	if err != nil {
		return "", err
	}

	text := resp.Text()
	if text != "" {
		result := strings.TrimSpace(text)
		log.Printf("Gemini search query response: %s", result)
		return result, nil
	}

	return "", fmt.Errorf("no content from gemini")
}

// craftSongList uses the Google GenAI SDK to ask Gemini for a short JSON array
// of recent/popular songs in the requested language.
// It returns a slice of {Title, Artist}.
func craftSongList(lang string) ([]struct{ Title, Artist string }, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey: os.Getenv("GEMINI_API_KEY"),
	})
	if err != nil {
		return nil, err
	}

	prompt := fmt.Sprintf(`Provide a JSON array of 10-15 popular and recent songs in the %s language from the last 2 years. 
For each song, include the title and artist name.
Return ONLY a valid JSON array like:
[{"title":"Song Title","artist":"Artist Name"}]

Requirements:
- Include only well-known official songs
- Avoid compilations, covers, remixes, and album uploads
- Prefer recent releases from the last 2 years
- One song per entry`, lang)

	resp, err := client.Models.GenerateContent(ctx, "gemini-2.5-flash", genai.Text(prompt), nil)
	if err != nil {
		return nil, err
	}

	text := strings.TrimSpace(resp.Text())

	if text == "" {
		return nil, fmt.Errorf("no content from gemini")
	}

	log.Printf("Gemini song list response: %s", short(text, 800))

	// Extract JSON from response (sometimes Gemini wraps it in markdown code blocks)
	// Remove markdown code blocks if present
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	if idx := strings.Index(text, "["); idx >= 0 {
		if end := strings.LastIndex(text, "]"); end > idx {
			text = text[idx : end+1]
		}
	}

	log.Printf("Extracted JSON (first 500 chars): %s", short(text, 500))

	// Try to parse JSON array
	var arr []map[string]interface{}
	if err := json.Unmarshal([]byte(text), &arr); err != nil {
		log.Printf("JSON parse failed: %v", err)
		log.Printf("Raw text that failed to parse: %s", short(text, 500))
		return nil, fmt.Errorf("could not parse song list: %v", err)
	}

	log.Printf("Successfully parsed JSON array with %d entries", len(arr))
	out := make([]struct{ Title, Artist string }, 0, len(arr))

	for _, it := range arr {
		t := ""
		a := ""

		// Try both lowercase and capitalized keys
		if v, ok := it["title"].(string); ok {
			t = v
		} else if v, ok := it["Title"].(string); ok {
			t = v
		}
		if v, ok := it["artist"].(string); ok {
			a = v
		} else if v, ok := it["Artist"].(string); ok {
			a = v
		}

		if t != "" {
			out = append(out, struct{ Title, Artist string }{Title: t, Artist: a})
		}
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no valid songs found")
	}

	log.Printf("Extracted %d valid songs from Gemini", len(out))
	return out, nil
}

func download10sClip(youtubeURL string, clipLength int) (string, error) {
	tmp, err := os.MkdirTemp("", "songclip")
	if err != nil {
		return "", err
	}
	log.Printf("downloading audio for %s into %s", youtubeURL, tmp)
	// download best audio using yt-dlp
	// prefer to suppress warnings which can leak into output
	cmd := exec.Command("yt-dlp", "--no-warnings", "-f", "bestaudio", "-o", "%(id)s.%(ext)s", youtubeURL)
	cmd.Dir = tmp
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("yt-dlp download error: %v", err)
		log.Printf("yt-dlp download output (truncated): %s", short(string(out), 800))
		return "", fmt.Errorf("yt-dlp error: %v - %s", err, string(out))
	} else {
		log.Printf("yt-dlp download output (truncated): %s", short(string(out), 800))
	}
	// find downloaded file
	files, _ := os.ReadDir(tmp)
	if len(files) == 0 {
		return "", fmt.Errorf("no file downloaded")
	}
	var inFile string
	for _, f := range files {
		if !f.IsDir() {
			inFile = filepath.Join(tmp, f.Name())
			break
		}
	}
	if inFile == "" {
		return "", fmt.Errorf("no input file")
	}

	outPath := filepath.Join(tmp, "clip.mp3")
	// trim to specified length (in seconds)
	cmd2 := exec.Command("ffmpeg", "-y", "-i", inFile, "-ss", "0", "-t", fmt.Sprintf("%d", clipLength), "-acodec", "libmp3lame", outPath)
	if out, err := cmd2.CombinedOutput(); err != nil {
		log.Printf("ffmpeg error: %v", err)
		log.Printf("ffmpeg output (truncated): %s", short(string(out), 800))
		return "", fmt.Errorf("ffmpeg error: %v - %s", err, string(out))
	} else {
		log.Printf("ffmpeg output (truncated): %s", short(string(out), 800))
	}
	return outPath, nil
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	setCORS(w)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func setCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

func short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func isBanned(s string, banned []string) bool {
	sl := strings.ToLower(s)
	for _, b := range banned {
		if strings.Contains(sl, b) {
			return true
		}
	}
	return false
}

func extractYouTubeID(u string) string {
	if u == "" {
		return ""
	}
	// try parse URL
	parsed, err := url.Parse(u)
	if err == nil {
		if parsed.Host == "youtu.be" {
			return strings.TrimPrefix(parsed.Path, "/")
		}
		q := parsed.Query()
		if v := q.Get("v"); v != "" {
			return v
		}
		// sometimes path contains /watch?v= in other forms
		if strings.Contains(parsed.Path, "/watch") {
			if v := q.Get("v"); v != "" {
				return v
			}
		}
	}
	// fallback: regex-ish search
	if idx := strings.Index(u, "v="); idx >= 0 {
		s := u[idx+2:]
		// stop at & or ?
		for i, ch := range s {
			if ch == '&' || ch == '?' {
				s = s[:i]
				break
			}
		}
		return s
	}
	return ""
}

// parseJSONWithRecovery attempts to unmarshal JSON from raw output,
// and if it fails, tries to locate the first '{' and retry.
func parseJSONWithRecovery(data []byte) (map[string]interface{}, error) {
	var info map[string]interface{}
	if err := json.Unmarshal(data, &info); err == nil {
		return info, nil
	}
	// try to recover by locating the first JSON brace
	s := string(data)
	idx := strings.Index(s, "{")
	if idx >= 0 {
		s2 := s[idx:]
		if err := json.Unmarshal([]byte(s2), &info); err == nil {
			return info, nil
		}
	}
	return nil, fmt.Errorf("could not parse JSON")
}

// getYouTubeDurationSeconds tries to fetch video metadata via yt-dlp and
// return the duration in seconds. If it cannot determine duration it
// returns an error. Callers may choose to treat unknown duration as keep.
func getYouTubeDurationSeconds(link string) (int, error) {
	if link == "" {
		return 0, fmt.Errorf("empty link")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "yt-dlp", "--no-warnings", "-J", link)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, err
	}
	info, err := parseJSONWithRecovery(out)
	if err != nil {
		return 0, err
	}
	if d, ok := info["duration"].(float64); ok {
		return int(d), nil
	}
	if d, ok := info["duration_seconds"].(float64); ok {
		return int(d), nil
	}
	if d, ok := info["length"].(float64); ok {
		return int(d), nil
	}
	return 0, fmt.Errorf("duration not found")
}
func isUsed(id string) bool {
	if id == "" {
		return false
	}
	usedMu.Lock()
	defer usedMu.Unlock()
	_, ok := usedVideos[id]
	return ok
}

func markUsed(id string) {
	if id == "" {
		return
	}
	usedMu.Lock()
	usedVideos[id] = struct{}{}
	usedMu.Unlock()
}

func randomID(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rng.Intn(len(letters))]
	}
	return string(b)
}
