package state

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"gorm.io/gorm"
)

var (
	ErrAIJobNotCancelable = errors.New("AI job is not cancelable")
	ErrAIJobNotRetryable  = errors.New("AI job is not retryable")
	ErrAIJobNotTerminal   = errors.New("AI job is not terminal")
)

type sealedAIProfile struct {
	model.AIProfile
	APIKey string `json:"api_key"`
}

type sealedAIJobConfig struct {
	Profile sealedAIProfile         `json:"profile"`
	Prompt  *model.AIPromptTemplate `json:"prompt,omitempty"`
}

func (s *Store) PutAIProfile(profile model.AIProfile) (model.AIProfile, error) {
	if err := profile.Validate(); err != nil {
		return model.AIProfile{}, err
	}
	if !profile.Enabled {
		profile.Default = false
	}
	now := time.Now()
	if profile.ID == "" {
		id, err := randomID()
		if err != nil {
			return model.AIProfile{}, err
		}
		profile.ID = id
		profile.CreatedAt = now
	} else {
		current, err := s.AIProfile(profile.ID)
		if err != nil {
			return model.AIProfile{}, err
		}
		profile.CreatedAt = current.CreatedAt
	}
	profile.Name = strings.TrimSpace(profile.Name)
	profile.BaseURL = strings.TrimRight(strings.TrimSpace(profile.BaseURL), "/")
	profile.Model = strings.TrimSpace(profile.Model)
	profile.UpdatedAt = now
	sealed, err := sealJSON(s.vault, tableAIProfiles, profile.ID, sealedAIProfile{AIProfile: profile, APIKey: profile.APIKey})
	if err != nil {
		return model.AIProfile{}, err
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if profile.Default {
			if err := tx.Model(&aiProfileRow{}).Where("kind = ?", profile.Kind).Update("is_default", 0).Error; err != nil {
				return err
			}
		}
		return tx.Save(&aiProfileRow{
			ID: profile.ID, Kind: string(profile.Kind), Name: profile.Name, Default: boolToInt(profile.Default), Enabled: boolToInt(profile.Enabled), Sealed: sealed,
			CreatedAt: profile.CreatedAt.Unix(), UpdatedAt: profile.UpdatedAt.Unix(),
		}).Error
	})
	return profile, err
}

func (s *Store) AIProfile(id string) (model.AIProfile, error) {
	var row aiProfileRow
	err := s.db.Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.AIProfile{}, ErrNotFound
	}
	if err != nil {
		return model.AIProfile{}, err
	}
	return decodeAIProfile(s, row)
}

func (s *Store) ListAIProfiles() ([]model.AIProfile, error) {
	var rows []aiProfileRow
	if err := s.db.Order("kind, is_default DESC, name").Find(&rows).Error; err != nil {
		return nil, err
	}
	profiles := make([]model.AIProfile, 0, len(rows))
	for _, row := range rows {
		profile, err := decodeAIProfile(s, row)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, nil
}

func decodeAIProfile(s *Store, row aiProfileRow) (model.AIProfile, error) {
	var sealed sealedAIProfile
	if err := openJSON(s.vault, tableAIProfiles, row.ID, row.Sealed, &sealed); err != nil {
		return model.AIProfile{}, err
	}
	sealed.AIProfile.APIKey = sealed.APIKey
	sealed.AIProfile.Enabled = row.Enabled != 0
	sealed.AIProfile.Default = row.Default != 0
	sealed.AIProfile.CreatedAt = time.Unix(row.CreatedAt, 0)
	sealed.AIProfile.UpdatedAt = time.Unix(row.UpdatedAt, 0)
	return sealed.AIProfile, nil
}

func (s *Store) SetAIProfileEnabled(id string, enabled bool) (model.AIProfile, error) {
	now := time.Now()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{"is_enabled": boolToInt(enabled), "updated_at": now.Unix()}
		if !enabled {
			updates["is_default"] = 0
		}
		result := tx.Model(&aiProfileRow{}).Where("id = ?", id).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return model.AIProfile{}, err
	}
	return s.AIProfile(id)
}

func (s *Store) DeleteAIProfile(id string) error {
	res := s.db.Where("id = ?", id).Delete(&aiProfileRow{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) PutAIPrompt(prompt model.AIPromptTemplate) (model.AIPromptTemplate, error) {
	if err := prompt.Validate(); err != nil {
		return model.AIPromptTemplate{}, err
	}
	now := time.Now()
	if prompt.ID == "" {
		id, err := randomID()
		if err != nil {
			return model.AIPromptTemplate{}, err
		}
		prompt.ID = id
		prompt.CreatedAt = now
	} else {
		current, err := s.AIPrompt(prompt.ID)
		if err != nil {
			return model.AIPromptTemplate{}, err
		}
		prompt.CreatedAt = current.CreatedAt
	}
	prompt.Name = strings.TrimSpace(prompt.Name)
	prompt.UpdatedAt = now
	sealed, err := sealJSON(s.vault, tableAIPrompts, prompt.ID, prompt)
	if err != nil {
		return model.AIPromptTemplate{}, err
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if prompt.Default {
			if err := tx.Model(&aiPromptRow{}).Where("1 = 1").Update("is_default", 0).Error; err != nil {
				return err
			}
		}
		return tx.Save(&aiPromptRow{ID: prompt.ID, Name: prompt.Name, Default: boolToInt(prompt.Default), Sealed: sealed, CreatedAt: prompt.CreatedAt.Unix(), UpdatedAt: prompt.UpdatedAt.Unix()}).Error
	})
	return prompt, err
}

func (s *Store) AIPrompt(id string) (model.AIPromptTemplate, error) {
	var row aiPromptRow
	err := s.db.Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.AIPromptTemplate{}, ErrNotFound
	}
	if err != nil {
		return model.AIPromptTemplate{}, err
	}
	return decodeAIPrompt(s, row)
}

func (s *Store) ListAIPrompts() ([]model.AIPromptTemplate, error) {
	var rows []aiPromptRow
	if err := s.db.Order("is_default DESC, name").Find(&rows).Error; err != nil {
		return nil, err
	}
	prompts := make([]model.AIPromptTemplate, 0, len(rows))
	for _, row := range rows {
		prompt, err := decodeAIPrompt(s, row)
		if err != nil {
			return nil, err
		}
		prompts = append(prompts, prompt)
	}
	return prompts, nil
}

func decodeAIPrompt(s *Store, row aiPromptRow) (model.AIPromptTemplate, error) {
	var prompt model.AIPromptTemplate
	if err := openJSON(s.vault, tableAIPrompts, row.ID, row.Sealed, &prompt); err != nil {
		return model.AIPromptTemplate{}, err
	}
	return prompt, nil
}

func (s *Store) DeleteAIPrompt(id string) error {
	var count int64
	if err := s.db.Model(&aiJobRow{}).Where("prompt_id = ?", id).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return errors.New("prompt is referenced by AI jobs")
	}
	res := s.db.Where("id = ?", id).Delete(&aiPromptRow{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CreateAIJob(job model.AIJob) (model.AIJob, bool, error) {
	if job.Kind != model.AIJobTranscription && job.Kind != model.AIJobSummary {
		return model.AIJob{}, false, errors.New("invalid AI job kind")
	}
	if strings.TrimSpace(job.ClientRequestID) == "" {
		return model.AIJob{}, false, errors.New("client_request_id is required")
	}
	profile, err := s.AIProfile(job.ProfileID)
	if err != nil {
		return model.AIJob{}, false, fmt.Errorf("loading AI profile: %w", err)
	}
	if job.Kind == model.AIJobSummary && job.PromptID == "" {
		return model.AIJob{}, false, errors.New("prompt_id is required for summary jobs")
	}
	var prompt *model.AIPromptTemplate
	if job.PromptID != "" {
		value, promptErr := s.AIPrompt(job.PromptID)
		if promptErr != nil {
			return model.AIJob{}, false, fmt.Errorf("loading AI prompt: %w", promptErr)
		}
		prompt = &value
	}
	var existing aiJobRow
	err = s.db.Where("client_request_id = ?", job.ClientRequestID).Take(&existing).Error
	if err == nil {
		decoded, decodeErr := s.aiJob(existing, true)
		return decoded, false, decodeErr
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.AIJob{}, false, err
	}
	id, err := randomID()
	if err != nil {
		return model.AIJob{}, false, err
	}
	now := time.Now()
	job.ID, job.State, job.Stage, job.Progress = id, model.AIJobQueued, "queued", 0
	job.CreatedAt, job.UpdatedAt = now, now
	input, err := sealJSON(s.vault, tableAIJobInput, id, job.Input)
	if err != nil {
		return model.AIJob{}, false, err
	}
	config, err := sealJSON(s.vault, tableAIJobConfig, id, sealedAIJobConfig{Profile: sealedAIProfile{AIProfile: profile, APIKey: profile.APIKey}, Prompt: prompt})
	if err != nil {
		return model.AIJob{}, false, err
	}
	row := aiJobRow{ID: id, ClientRequestID: job.ClientRequestID, Kind: string(job.Kind), State: string(job.State), Stage: job.Stage, ProfileID: job.ProfileID, PromptID: job.PromptID, InputSealed: input, ConfigSealed: config, CreatedAt: now.Unix(), UpdatedAt: now.Unix()}
	if err := s.db.Create(&row).Error; err != nil {
		if lookupErr := s.db.Where("client_request_id = ?", job.ClientRequestID).Take(&existing).Error; lookupErr == nil {
			decoded, decodeErr := s.aiJob(existing, true)
			return decoded, false, decodeErr
		}
		return model.AIJob{}, false, err
	}
	return job, true, nil
}

func (s *Store) AIJobConfig(id string) (model.AIProfile, *model.AIPromptTemplate, error) {
	var row aiJobRow
	err := s.db.Select("id", "config_sealed").Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.AIProfile{}, nil, ErrNotFound
	}
	if err != nil {
		return model.AIProfile{}, nil, err
	}
	var config sealedAIJobConfig
	if err := openJSON(s.vault, tableAIJobConfig, row.ID, row.ConfigSealed, &config); err != nil {
		return model.AIProfile{}, nil, err
	}
	config.Profile.AIProfile.APIKey = config.Profile.APIKey
	return config.Profile.AIProfile, config.Prompt, nil
}

func (s *Store) AIJob(id string) (model.AIJob, error) {
	var row aiJobRow
	err := s.db.Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.AIJob{}, ErrNotFound
	}
	if err != nil {
		return model.AIJob{}, err
	}
	return s.aiJob(row, true)
}

func (s *Store) ListAIJobs(query model.AIJobQuery) (model.AIJobPage, error) {
	if query.Limit <= 0 {
		query.Limit = 20
	}
	if query.Limit > 100 {
		query.Limit = 100
	}
	if query.Offset < 0 {
		return model.AIJobPage{}, errors.New("offset cannot be negative")
	}
	db := s.db.Model(&aiJobRow{})
	if query.Kind != "" {
		db = db.Where("kind = ?", query.Kind)
	}
	if query.State != "" {
		db = db.Where("state = ?", query.State)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return model.AIJobPage{}, err
	}
	var rows []aiJobRow
	if err := db.Order("created_at DESC, id DESC").Limit(query.Limit).Offset(query.Offset).Find(&rows).Error; err != nil {
		return model.AIJobPage{}, err
	}
	items := make([]model.AIJob, 0, len(rows))
	for _, row := range rows {
		job, err := s.aiJob(row, false)
		if err != nil {
			return model.AIJobPage{}, err
		}
		items = append(items, job)
	}
	return model.AIJobPage{Items: items, Total: total, Limit: query.Limit, Offset: query.Offset}, nil
}

func (s *Store) aiJob(row aiJobRow, detail bool) (model.AIJob, error) {
	job := model.AIJob{
		ID: row.ID, ClientRequestID: row.ClientRequestID, Kind: model.AIJobKind(row.Kind), State: model.AIJobState(row.State),
		Stage: row.Stage, Progress: row.Progress, ProfileID: row.ProfileID, PromptID: row.PromptID, Attempts: row.Attempts,
		ErrorCode: row.ErrorCode, LastError: row.LastError, CreatedAt: time.Unix(row.CreatedAt, 0), UpdatedAt: time.Unix(row.UpdatedAt, 0),
	}
	if row.StartedAt != nil {
		job.StartedAt = time.Unix(*row.StartedAt, 0)
	}
	if row.FinishedAt != nil {
		job.FinishedAt = time.Unix(*row.FinishedAt, 0)
	}
	if !detail {
		return job, nil
	}
	switch job.Kind {
	case model.AIJobTranscription:
		var input model.AITranscriptionInput
		if err := openJSON(s.vault, tableAIJobInput, row.ID, row.InputSealed, &input); err != nil {
			return model.AIJob{}, err
		}
		job.Input = input
		if len(row.ResultSealed) > 0 {
			var result model.AITranscriptionResult
			if err := openJSON(s.vault, tableAIJobResult, row.ID, row.ResultSealed, &result); err != nil {
				return model.AIJob{}, err
			}
			job.Result = result
		}
	case model.AIJobSummary:
		var input model.AISummaryInput
		if err := openJSON(s.vault, tableAIJobInput, row.ID, row.InputSealed, &input); err != nil {
			return model.AIJob{}, err
		}
		job.Input = input
		if len(row.ResultSealed) > 0 {
			var result model.AISummaryResult
			if err := openJSON(s.vault, tableAIJobResult, row.ID, row.ResultSealed, &result); err != nil {
				return model.AIJob{}, err
			}
			job.Result = result
		}
	}
	return job, nil
}

func (s *Store) ClaimAIJob(kind model.AIJobKind) (model.AIJob, error) {
	var claimed aiJobRow
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("state = ? AND kind = ?", model.AIJobQueued, kind).Order("created_at, id").Take(&claimed).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		now := time.Now().Unix()
		res := tx.Model(&aiJobRow{}).Where("id = ? AND state = ?", claimed.ID, model.AIJobQueued).Updates(map[string]any{
			"state": model.AIJobRunning, "stage": "starting", "attempts": gorm.Expr("attempts + 1"), "started_at": now, "finished_at": nil, "updated_at": now, "error_code": "", "last_error": "",
		})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrNotFound
		}
		return tx.Where("id = ?", claimed.ID).Take(&claimed).Error
	})
	if err != nil {
		return model.AIJob{}, err
	}
	return s.aiJob(claimed, true)
}

func (s *Store) UpdateAIJobProgress(id, stage string, progress int) error {
	if progress < 0 || progress > 100 || strings.TrimSpace(stage) == "" {
		return errors.New("invalid AI job progress")
	}
	res := s.db.Model(&aiJobRow{}).Where("id = ? AND state = ?", id, model.AIJobRunning).Updates(map[string]any{"stage": stage, "progress": progress, "updated_at": time.Now().Unix()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) FinishAIJob(id string, result any) error {
	sealed, err := sealJSON(s.vault, tableAIJobResult, id, result)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	res := s.db.Model(&aiJobRow{}).Where("id = ? AND state = ?", id, model.AIJobRunning).Updates(map[string]any{
		"state": model.AIJobSucceeded, "stage": "completed", "progress": 100, "result_sealed": sealed, "finished_at": now, "updated_at": now,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) FailAIJob(id, code, message string) error {
	now := time.Now().Unix()
	res := s.db.Model(&aiJobRow{}).Where("id = ? AND state = ?", id, model.AIJobRunning).Updates(map[string]any{
		"state": model.AIJobFailed, "stage": "failed", "error_code": code, "last_error": message, "finished_at": now, "updated_at": now,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CancelAIJob(id string) error {
	now := time.Now().Unix()
	res := s.db.Model(&aiJobRow{}).Where("id = ? AND state IN ?", id, []model.AIJobState{model.AIJobQueued, model.AIJobRunning}).Updates(map[string]any{
		"state": model.AIJobCanceled, "stage": "canceled", "finished_at": now, "updated_at": now,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrAIJobNotCancelable
	}
	return nil
}

func (s *Store) RetryAIJob(id string) error {
	res := s.db.Model(&aiJobRow{}).Where("id = ? AND state IN ?", id, []model.AIJobState{model.AIJobFailed, model.AIJobCanceled}).Updates(map[string]any{
		"state": model.AIJobQueued, "stage": "queued", "progress": 0, "error_code": "", "last_error": "", "started_at": nil, "finished_at": nil, "updated_at": time.Now().Unix(),
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrAIJobNotRetryable
	}
	return nil
}

func (s *Store) DeleteAIJob(id string) error {
	res := s.db.Where("id = ? AND state IN ?", id, []model.AIJobState{model.AIJobSucceeded, model.AIJobFailed, model.AIJobCanceled}).Delete(&aiJobRow{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrAIJobNotTerminal
	}
	return nil
}

func (s *Store) InterruptRunningAIJobs() (int64, error) {
	now := time.Now().Unix()
	res := s.db.Model(&aiJobRow{}).Where("state = ?", model.AIJobRunning).Updates(map[string]any{
		"state": model.AIJobFailed, "stage": "failed", "error_code": "worker_interrupted", "last_error": "the process stopped while the AI job was running", "finished_at": now, "updated_at": now,
	})
	return res.RowsAffected, res.Error
}
