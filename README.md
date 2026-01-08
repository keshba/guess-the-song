# Guess the Song

A modern music guessing game powered by AI. The backend uses Google Gemini to curate popular songs in multiple languages, then searches YouTube for official music videos. A beautiful React frontend lets you play clips and guess songs with adjustable clip length.

## Features

- **Multi-language Support**: English, Hindi, Tamil (extensible)
- **AI-Powered Song Selection**: Uses Google Gemini API to get curated lists of popular recent songs
- **Smart Song Caching**: Fetches 15 songs at a time from Gemini, reuses them across rounds before refreshing
- **Adjustable Clip Length**: Choose from 10-60 seconds (default 30s)
- **Official Videos Only**: Filters for single-song music videos, avoids compilations and remixes
- **YouTube Integration**: Links to full songs via YouTube oEmbed
- **Beautiful Modern UI**: Dark theme with glassmorphism effects, Material Design icons
- **Real-time Language Switching**: Change language and instantly load new song cache

## Prerequisites

- **Go 1.18+**
- **yt-dlp** (for YouTube video search/download) - install via: `pip install yt-dlp`
- **ffmpeg** (for audio trimming) - download from [ffmpeg.org](https://ffmpeg.org)
- **GEMINI_API_KEY** - Get a free API key from [Google AI Studio](https://aistudio.google.com/app/apikey)
- **SERPAPI_API_KEY** (optional) - For better search fallback results

## Setup

### 1. Install Dependencies

```powershell
# Install yt-dlp
pip install yt-dlp

# Install ffmpeg (on Windows, or use your package manager)
# Download from https://ffmpeg.org/download.html
```

### 2. Set Environment Variables

```powershell
# Required
$env:GEMINI_API_KEY="your-gemini-api-key-here"

# Optional (improves fallback search if Gemini songs don't yield results)
$env:SERPAPI_API_KEY="your-serpapi-key-here"
```

### 3. Run the Backend

```powershell
# From the repository root
go build -o songs_ai_agent.exe songs_ai_agent.go
./songs_ai_agent.exe

# Or run directly
go run songs_ai_agent.go
```

The server starts on `http://localhost:8080`.

### 4. Run the Frontend

```powershell
# From the frontend directory, serve via Python
python -m http.server 5173 -d frontend

# Or open directly in browser
# file:///path/to/frontend/index.html
```

Open `http://localhost:5173` in your browser.

## API Endpoints

### Core Game Endpoints

- **`GET /start?lang=<language>&clipLength=<seconds>`**
  - Starts a new round with specified language and clip length
  - Returns: `{id, clip_url}`
  - Example: `/start?lang=hindi&clipLength=25`

- **`GET /clip?id=<id>`**
  - Serves the audio clip (audio/mpeg format)
  - Blocks until clip is ready (with 30s timeout)

- **`GET /status?id=<id>`**
  - Check if clip is ready: `{ready: bool, error: string}`

- **`POST /guess`**
  - Submit a guess: `{id, guess}`
  - Returns: `{correct: bool}`
  - Forgiving matching: guess appears in title/artist or vice versa

- **`GET /reveal?id=<id>`**
  - Reveal the answer: `{title, artist, youtube}`

### Cache Management

- **`GET /refreshCache?lang=<language>`**
  - Force refresh of song cache for a language
  - Calls Gemini to fetch 15 new songs
  - Returns: `{status, songs_loaded}`

## How It Works

1. **Song Caching**: On first use or language change, Gemini is called to get 15 popular songs for that language
2. **Song Search**: For each cached song, yt-dlp searches YouTube for an official music video
3. **Validation**: Results are filtered by:
   - Duration (20-480 seconds, avoids albums/compilations)
   - Banned keywords (mix, compilation, medley, playlist, etc.)
   - Previously used videos (won't repeat)
4. **Clip Generation**: ffmpeg trims the audio to the specified length (10-60s)
5. **Cache Refresh**: After all 15 cached songs are used, a new Gemini call fetches the next batch

## Project Structure

```
.
├── songs_ai_agent.go           # Go backend server
├── go.mod                      # Go module file
├── frontend/
│   ├── index.html              # React app (CDN-based, no build needed)
└── README.md                   # This file
```

## Development Notes

- **Frontend**: Uses React 18 from CDN + Babel standalone transpiler. No build step needed!
- **Backend**: Single-file Go application (~850 lines)
- **Song Discovery**: Prioritizes Gemini's curated lists over generic YouTube search
- **Fallback**: If Gemini API is unavailable, falls back to SerpAPI or yt-dlp search

## Configuration

In `songs_ai_agent.go`, you can modify:

- `bannedKeywords`: List of keywords to filter out (compilations, mixes, etc.)
- `clipLength` range: Line `if parsed, err := strconv.Atoi(cl); err == nil && parsed > 0 && parsed <= 300`
- Cache size: Change `10-15` in Gemini prompts to cache more/fewer songs per batch

## Troubleshooting

**"GEMINI_API_KEY not set"**
- Set the environment variable before running the server

**"yt-dlp search error"**
- Ensure yt-dlp is installed: `pip install --upgrade yt-dlp`
- Check that it's on PATH: `yt-dlp --version`

**"ffmpeg error"**
- Ensure ffmpeg is installed and on PATH: `ffmpeg -version`

**"No usable songs found"**
- Try a different language or wait a moment for cache to refresh
- Check server logs for Gemini/yt-dlp errors

## Future Enhancements

- Database to track user scores and statistics
- Difficulty levels (shorter/longer clips, harder songs)
- Multiplayer/leaderboard support
- Additional languages
- Offline mode with pre-cached songs
- Custom playlist support

Run the backend

```powershell
# from repository root
go run songs_ai_agent.go
```

This starts a server on `http://localhost:8080` with endpoints:
- `GET /start?lang=<language>` — starts a new round; returns `{id, clip_url}`
- `GET /clip?id=<id>` — serves the 10s audio clip (audio/mpeg)
- `POST /guess` — JSON `{id, guess}` returns `{correct: true|false}`
- `GET /reveal?id=<id>` — returns `{title, artist, youtube}`

Open the frontend

Open `frontend/index.html` in your browser (double-click or serve it). It uses CDN React + Babel for development.

Notes
- The backend uses `yt-dlp` and `ffmpeg` to download and trim audio; ensure both are installed.
- If you want better search results, set `SERPAPI_API_KEY` before starting the backend.
- This setup is for local development and demo purposes only.
