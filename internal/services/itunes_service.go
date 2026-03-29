package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type ITunesService struct {
	client *http.Client
}

func NewITunesService() *ITunesService {
	return &ITunesService{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

type ITunesTrack struct {
	TrackName  string `json:"track_name"`
	ArtistName string `json:"artist_name"`
	ArtworkURL string `json:"artwork_url"`
}

type itunesAPIResponse struct {
	ResultCount int `json:"resultCount"`
	Results     []struct {
		TrackName     string `json:"trackName"`
		ArtistName    string `json:"artistName"`
		ArtworkUrl100 string `json:"artworkUrl100"`
	} `json:"results"`
}

func (s *ITunesService) Search(ctx context.Context, query string, limit int) ([]ITunesTrack, error) {
	if limit <= 0 || limit > 10 {
		limit = 5
	}

	apiURL := fmt.Sprintf("https://itunes.apple.com/search?term=%s&media=music&limit=%d",
		url.QueryEscape(query), limit)

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("iTunes search failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read iTunes response: %w", err)
	}

	var apiResp itunesAPIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to parse iTunes response: %w", err)
	}

	tracks := make([]ITunesTrack, 0, len(apiResp.Results))
	for _, r := range apiResp.Results {
		tracks = append(tracks, ITunesTrack{
			TrackName:  r.TrackName,
			ArtistName: r.ArtistName,
			ArtworkURL: r.ArtworkUrl100,
		})
	}

	return tracks, nil
}
