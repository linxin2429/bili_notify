package model

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

type AIProfileKind string

const (
	AIProfileTranscription AIProfileKind = "transcription"
	AIProfileText          AIProfileKind = "text"
)

type AIProfile struct {
	ID                 string        `json:"id"`
	Name               string        `json:"name"`
	Kind               AIProfileKind `json:"kind"`
	BaseURL            string        `json:"base_url"`
	Model              string        `json:"model"`
	APIKey             string        `json:"-"`
	Language           string        `json:"language,omitempty"`
	Prompt             string        `json:"prompt,omitempty"`
	Temperature        float64       `json:"temperature,omitempty"`
	MaxOutputTokens    int           `json:"max_output_tokens,omitempty"`
	ContextWindowChars int           `json:"context_window_chars,omitempty"`
	TimeoutSec         int           `json:"timeout_sec"`
	Default            bool          `json:"default"`
	CreatedAt          time.Time     `json:"created_at"`
	UpdatedAt          time.Time     `json:"updated_at"`
}

func (p AIProfile) Validate() error {
	var errs []error
	if strings.TrimSpace(p.Name) == "" {
		errs = append(errs, errors.New("profile name is required"))
	}
	if p.Kind != AIProfileTranscription && p.Kind != AIProfileText {
		errs = append(errs, fmt.Errorf("unsupported profile kind %q", p.Kind))
	}
	u, err := url.Parse(strings.TrimSpace(p.BaseURL))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		errs = append(errs, errors.New("base_url must be an absolute HTTPS URL without credentials, query, or fragment"))
	}
	if strings.TrimSpace(p.Model) == "" {
		errs = append(errs, errors.New("model is required"))
	}
	if strings.TrimSpace(p.APIKey) == "" {
		errs = append(errs, errors.New("api_key is required"))
	}
	if p.TimeoutSec < 10 || p.TimeoutSec > 24*60*60 {
		errs = append(errs, errors.New("timeout_sec must be in [10, 86400]"))
	}
	if p.Kind == AIProfileText {
		if p.Temperature < 0 || p.Temperature > 2 {
			errs = append(errs, errors.New("temperature must be in [0, 2]"))
		}
		if p.MaxOutputTokens < 1 || p.MaxOutputTokens > 131072 {
			errs = append(errs, errors.New("max_output_tokens must be in [1, 131072]"))
		}
		if p.ContextWindowChars < 1000 || p.ContextWindowChars > 4<<20 {
			errs = append(errs, errors.New("context_window_chars must be in [1000, 4194304]"))
		}
	}
	return errors.Join(errs...)
}

type AIPromptTemplate struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	SystemPrompt string    `json:"system_prompt"`
	ChunkPrompt  string    `json:"chunk_prompt"`
	ReducePrompt string    `json:"reduce_prompt"`
	Default      bool      `json:"default"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (p AIPromptTemplate) Validate() error {
	var errs []error
	if strings.TrimSpace(p.Name) == "" {
		errs = append(errs, errors.New("prompt name is required"))
	}
	if strings.Count(p.ChunkPrompt, "{{text}}") != 1 {
		errs = append(errs, errors.New("chunk_prompt must contain {{text}} exactly once"))
	}
	if strings.Count(p.ReducePrompt, "{{summaries}}") != 1 {
		errs = append(errs, errors.New("reduce_prompt must contain {{summaries}} exactly once"))
	}
	return errors.Join(errs...)
}

type AIJobKind string
type AIJobState string

const (
	AIJobTranscription AIJobKind = "transcription"
	AIJobSummary       AIJobKind = "summary"

	AIJobQueued    AIJobState = "queued"
	AIJobRunning   AIJobState = "running"
	AIJobSucceeded AIJobState = "succeeded"
	AIJobFailed    AIJobState = "failed"
	AIJobCanceled  AIJobState = "canceled"
)

type AITranscriptionInput struct {
	BVID string `json:"bvid"`
	Page int    `json:"page,omitempty"`
}

type AISummaryInput struct {
	Text            string `json:"text,omitempty"`
	TranscriptionID string `json:"transcription_job_id,omitempty"`
}

type AITranscriptSegment struct {
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Text    string `json:"text"`
}

type AITranscriptPage struct {
	Page       int                   `json:"page"`
	CID        string                `json:"cid,omitempty"`
	Title      string                `json:"title"`
	DurationMS int64                 `json:"duration_ms"`
	Segments   []AITranscriptSegment `json:"segments"`
}

type AITranscriptionResult struct {
	BVID  string             `json:"bvid"`
	Title string             `json:"title"`
	Pages []AITranscriptPage `json:"pages"`
	Usage map[string]any     `json:"usage,omitempty"`
}

func (r AITranscriptionResult) Text() string {
	var b strings.Builder
	for _, page := range r.Pages {
		for _, segment := range page.Segments {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(segment.Text)
		}
	}
	return b.String()
}

type AISummaryResult struct {
	Markdown string         `json:"markdown"`
	Usage    map[string]any `json:"usage,omitempty"`
}

type AIJob struct {
	ID              string     `json:"id"`
	ClientRequestID string     `json:"client_request_id,omitempty"`
	Kind            AIJobKind  `json:"kind"`
	State           AIJobState `json:"state"`
	Stage           string     `json:"stage"`
	Progress        int        `json:"progress"`
	ProfileID       string     `json:"profile_id"`
	PromptID        string     `json:"prompt_id,omitempty"`
	Attempts        int        `json:"attempts"`
	ErrorCode       string     `json:"error_code,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	Input           any        `json:"input,omitempty"`
	Result          any        `json:"result,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       time.Time  `json:"started_at,omitzero"`
	FinishedAt      time.Time  `json:"finished_at,omitzero"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (j AIJob) Terminal() bool {
	return j.State == AIJobSucceeded || j.State == AIJobFailed || j.State == AIJobCanceled
}

type AIJobQuery struct {
	Kind   AIJobKind
	State  AIJobState
	Limit  int
	Offset int
}

type AIJobPage struct {
	Items  []AIJob `json:"items"`
	Total  int64   `json:"total"`
	Limit  int     `json:"limit"`
	Offset int     `json:"offset"`
}
