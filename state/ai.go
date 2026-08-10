package state

import (
	"encoding/json"
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
		if err := tx.Save(&aiProfileRow{
			ID: profile.ID, Kind: string(profile.Kind), Name: profile.Name, Default: boolToInt(profile.Default), Enabled: boolToInt(profile.Enabled), Sealed: sealed,
			CreatedAt: profile.CreatedAt.Unix(), UpdatedAt: profile.UpdatedAt.Unix(),
		}).Error; err != nil {
			return err
		}
		return s.ensureAutoAIInvariantTx(tx)
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
		return s.ensureAutoAIInvariantTx(tx)
	})
	if err != nil {
		return model.AIProfile{}, err
	}
	return s.AIProfile(id)
}

func (s *Store) DeleteAIProfile(id string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Where("id = ?", id).Delete(&aiProfileRow{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		return s.ensureAutoAIInvariantTx(tx)
	})
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
		if err := tx.Save(&aiPromptRow{ID: prompt.ID, Name: prompt.Name, Default: boolToInt(prompt.Default), Sealed: sealed, CreatedAt: prompt.CreatedAt.Unix(), UpdatedAt: prompt.UpdatedAt.Unix()}).Error; err != nil {
			return err
		}
		return s.ensureAutoAIInvariantTx(tx)
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
	return s.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Where("id = ?", id).Delete(&aiPromptRow{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		return s.ensureAutoAIInvariantTx(tx)
	})
}

func (s *Store) ensureAutoAIInvariantTx(tx *gorm.DB) error {
	enabled, err := automaticAIEnabledTx(tx)
	if err != nil || !enabled {
		return err
	}
	_, _, _, err = s.defaultAIConfigTx(tx)
	return err
}

func automaticAIEnabledTx(tx *gorm.DB) (bool, error) {
	var row metaRow
	if err := tx.Where("key = ?", metaKeyRuntimeSettings).Take(&row).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	var record runtimeSettingsRecord
	if err := json.Unmarshal([]byte(row.Value), &record); err != nil {
		return false, err
	}
	if record.Version != runtimeSettingsVersion {
		return false, ErrRuntimeSettingsVersionMismatch
	}
	return record.AIAutoProcessingEnabled, nil
}

func (s *Store) CreateAIJob(job model.AIJob) (model.AIJob, bool, error) {
	if job.Kind != model.AIJobTranscription && job.Kind != model.AIJobSummary {
		return model.AIJob{}, false, errors.New("invalid AI job kind")
	}
	if strings.TrimSpace(job.ClientRequestID) == "" {
		return model.AIJob{}, false, errors.New("client_request_id is required")
	}
	if job.Origin == "" {
		job.Origin = model.AIJobOriginWorkbench
	}
	job.OriginTraceparent = originTraceparent(s.db.Statement.Context)
	job.OriginTracestate = originTracestate(s.db.Statement.Context)
	var created model.AIJob
	var wasCreated bool
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var createErr error
		created, wasCreated, createErr = s.createAIJobTx(tx, job, nil, nil)
		return createErr
	})
	return created, wasCreated, err
}

func (s *Store) createAIJobTx(tx *gorm.DB, job model.AIJob, suppliedProfile *model.AIProfile, suppliedPrompt *model.AIPromptTemplate) (model.AIJob, bool, error) {
	var existing aiJobRow
	if err := tx.Where("client_request_id = ?", job.ClientRequestID).Take(&existing).Error; err == nil {
		decoded, decodeErr := s.aiJob(existing, true)
		return decoded, false, decodeErr
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.AIJob{}, false, err
	}
	profile := model.AIProfile{}
	if suppliedProfile != nil {
		profile = *suppliedProfile
	} else {
		var row aiProfileRow
		if err := tx.Where("id = ?", job.ProfileID).Take(&row).Error; err != nil {
			return model.AIJob{}, false, fmt.Errorf("loading AI profile: %w", err)
		}
		var err error
		profile, err = decodeAIProfile(s, row)
		if err != nil {
			return model.AIJob{}, false, err
		}
	}
	if job.Kind == model.AIJobSummary && job.PromptID == "" {
		return model.AIJob{}, false, errors.New("prompt_id is required for summary jobs")
	}
	var prompt *model.AIPromptTemplate
	if suppliedPrompt != nil {
		value := *suppliedPrompt
		prompt = &value
	} else if job.PromptID != "" {
		var row aiPromptRow
		if err := tx.Where("id = ?", job.PromptID).Take(&row).Error; err != nil {
			return model.AIJob{}, false, fmt.Errorf("loading AI prompt: %w", err)
		}
		value, err := decodeAIPrompt(s, row)
		if err != nil {
			return model.AIJob{}, false, err
		}
		prompt = &value
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
	channels, err := json.Marshal(job.TargetChannelIDs)
	if err != nil {
		return model.AIJob{}, false, err
	}
	row := aiJobRow{
		ID: id, ClientRequestID: job.ClientRequestID, Kind: string(job.Kind), State: string(job.State), Stage: job.Stage,
		ProfileID: job.ProfileID, PromptID: job.PromptID, Origin: string(job.Origin), SourceDynamicID: job.SourceDynamicID,
		DependsOnJobID: job.DependsOnJobID, OriginTraceparent: job.OriginTraceparent, OriginTracestate: job.OriginTracestate,
		TargetChannelIDs: string(channels), InputSealed: input, ConfigSealed: config, CreatedAt: now.Unix(), UpdatedAt: now.Unix(),
	}
	if err := tx.Create(&row).Error; err != nil {
		if lookupErr := tx.Where("client_request_id = ?", job.ClientRequestID).Take(&existing).Error; lookupErr == nil {
			decoded, decodeErr := s.aiJob(existing, true)
			return decoded, false, decodeErr
		}
		return model.AIJob{}, false, err
	}
	return job, true, nil
}

// ValidateAutoAIConfiguration verifies the three defaults required by automatic processing.
func (s *Store) ValidateAutoAIConfiguration() error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		_, _, _, err := s.defaultAIConfigTx(tx)
		return err
	})
}

func (s *Store) defaultAIConfigTx(tx *gorm.DB) (model.AIProfile, model.AIProfile, model.AIPromptTemplate, error) {
	var transcriptionRow, summaryRow aiProfileRow
	if err := tx.Where("kind = ? AND is_default = 1 AND is_enabled = 1", model.AIProfileTranscription).Take(&transcriptionRow).Error; err != nil {
		return model.AIProfile{}, model.AIProfile{}, model.AIPromptTemplate{}, errors.New("enabled default transcription profile is required")
	}
	if err := tx.Where("kind = ? AND is_default = 1 AND is_enabled = 1", model.AIProfileText).Take(&summaryRow).Error; err != nil {
		return model.AIProfile{}, model.AIProfile{}, model.AIPromptTemplate{}, errors.New("enabled default summary profile is required")
	}
	var promptRow aiPromptRow
	if err := tx.Where("is_default = 1").Take(&promptRow).Error; err != nil {
		return model.AIProfile{}, model.AIProfile{}, model.AIPromptTemplate{}, errors.New("default AI prompt is required")
	}
	transcription, err := decodeAIProfile(s, transcriptionRow)
	if err != nil {
		return model.AIProfile{}, model.AIProfile{}, model.AIPromptTemplate{}, err
	}
	summary, err := decodeAIProfile(s, summaryRow)
	if err != nil {
		return model.AIProfile{}, model.AIProfile{}, model.AIPromptTemplate{}, err
	}
	prompt, err := decodeAIPrompt(s, promptRow)
	return transcription, summary, prompt, err
}

func (s *Store) createAutomaticAIJobsTx(tx *gorm.DB, dynamic model.Dynamic, channelIDs []string) (int, error) {
	if dynamic.Type != "DYNAMIC_TYPE_AV" || strings.TrimSpace(dynamic.BVID) == "" {
		return 0, nil
	}
	transcriptionProfile, summaryProfile, prompt, err := s.defaultAIConfigTx(tx)
	if err != nil {
		return 0, err
	}
	traceparent, tracestate := originTraceparent(tx.Statement.Context), originTracestate(tx.Statement.Context)
	transcription, created, err := s.createAIJobTx(tx, model.AIJob{
		ClientRequestID: "dynamic:" + dynamic.ID + ":transcription", Kind: model.AIJobTranscription,
		ProfileID: transcriptionProfile.ID, Origin: model.AIJobOriginDynamic, SourceDynamicID: dynamic.ID,
		OriginTraceparent: traceparent, OriginTracestate: tracestate, TargetChannelIDs: channelIDs,
		Input: model.AITranscriptionInput{BVID: dynamic.BVID},
	}, &transcriptionProfile, nil)
	if err != nil {
		return 0, err
	}
	_, summaryCreated, err := s.createAIJobTx(tx, model.AIJob{
		ClientRequestID: "dynamic:" + dynamic.ID + ":summary", Kind: model.AIJobSummary,
		ProfileID: summaryProfile.ID, PromptID: prompt.ID, Origin: model.AIJobOriginDynamic,
		SourceDynamicID: dynamic.ID, DependsOnJobID: transcription.ID, OriginTraceparent: traceparent,
		OriginTracestate: tracestate, TargetChannelIDs: channelIDs,
		Input: model.AISummaryInput{TranscriptionID: transcription.ID},
	}, &summaryProfile, &prompt)
	if err != nil {
		return 0, err
	}
	count := 0
	if created {
		count++
	}
	if summaryCreated {
		count++
	}
	return count, nil
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
	var channels []string
	if err := json.Unmarshal([]byte(row.TargetChannelIDs), &channels); err != nil {
		return model.AIJob{}, fmt.Errorf("decoding AI job target channels: %w", err)
	}
	job := model.AIJob{
		ID: row.ID, ClientRequestID: row.ClientRequestID, Kind: model.AIJobKind(row.Kind), State: model.AIJobState(row.State),
		Stage: row.Stage, Progress: row.Progress, ProfileID: row.ProfileID, PromptID: row.PromptID, Attempts: row.Attempts,
		Origin: model.AIJobOrigin(row.Origin), SourceDynamicID: row.SourceDynamicID, DependsOnJobID: row.DependsOnJobID,
		OriginTraceparent: row.OriginTraceparent, OriginTracestate: row.OriginTracestate, TargetChannelIDs: channels,
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
		query := `state = ? AND kind = ? AND (depends_on_job_id = '' OR EXISTS (
			SELECT 1 FROM ai_jobs dependency WHERE dependency.id = ai_jobs.depends_on_job_id AND dependency.state = ?))`
		if err := tx.Where(query, model.AIJobQueued, kind, model.AIJobSucceeded).Order("created_at, id").Take(&claimed).Error; err != nil {
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

// UpdateDependentAITrace makes a dependent job continue the current execution span.
func (s *Store) UpdateDependentAITrace(id, traceparent, tracestate string) error {
	return s.db.Model(&aiJobRow{}).Where("depends_on_job_id = ? AND state = ?", id, model.AIJobQueued).
		Updates(map[string]any{"origin_traceparent": traceparent, "origin_tracestate": tracestate, "updated_at": time.Now().Unix()}).Error
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
	return s.db.Transaction(func(tx *gorm.DB) error {
		var row aiJobRow
		if err := tx.Where("id = ? AND state = ?", id, model.AIJobRunning).Take(&row).Error; err != nil {
			return ErrNotFound
		}
		now := time.Now().Unix()
		res := tx.Model(&aiJobRow{}).Where("id = ? AND state = ?", id, model.AIJobRunning).Updates(map[string]any{
			"state": model.AIJobSucceeded, "stage": "completed", "progress": 100, "result_sealed": sealed, "finished_at": now, "updated_at": now,
		})
		if res.Error != nil {
			return res.Error
		}
		return s.enqueueAINotificationTx(tx, row, result, true, "", "")
	})
}

func (s *Store) FailAIJob(id, code, message string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var row aiJobRow
		if err := tx.Where("id = ? AND state = ?", id, model.AIJobRunning).Take(&row).Error; err != nil {
			return ErrNotFound
		}
		now := time.Now().Unix()
		if err := tx.Model(&aiJobRow{}).Where("id = ? AND state = ?", id, model.AIJobRunning).Updates(map[string]any{
			"state": model.AIJobFailed, "stage": "failed", "error_code": code, "last_error": message, "finished_at": now, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&aiJobRow{}).Where("depends_on_job_id = ? AND state = ?", id, model.AIJobQueued).Updates(map[string]any{
			"state": model.AIJobSkipped, "stage": "skipped", "error_code": "dependency_failed", "last_error": "source transcription failed", "finished_at": now, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		return s.enqueueAINotificationTx(tx, row, nil, false, code, message)
	})
}

func (s *Store) CancelAIJob(id string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		now := time.Now().Unix()
		res := tx.Model(&aiJobRow{}).Where("id = ? AND state IN ?", id, []model.AIJobState{model.AIJobQueued, model.AIJobRunning}).Updates(map[string]any{
			"state": model.AIJobCanceled, "stage": "canceled", "finished_at": now, "updated_at": now,
		})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrAIJobNotCancelable
		}
		return tx.Model(&aiJobRow{}).Where("depends_on_job_id = ? AND state = ?", id, model.AIJobQueued).Updates(map[string]any{
			"state": model.AIJobSkipped, "stage": "skipped", "error_code": "dependency_canceled", "last_error": "source transcription was canceled", "finished_at": now, "updated_at": now,
		}).Error
	})
}

func (s *Store) RetryAIJob(id string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var row aiJobRow
		if err := tx.Select("id", "origin").Where("id = ? AND state IN ?", id, []model.AIJobState{model.AIJobFailed, model.AIJobCanceled, model.AIJobSkipped}).Take(&row).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrAIJobNotRetryable
		} else if err != nil {
			return err
		}
		now := time.Now().Unix()
		updates := map[string]any{
			"state": model.AIJobQueued, "stage": "queued", "progress": 0, "error_code": "", "last_error": "", "started_at": nil, "finished_at": nil,
			"updated_at": now,
		}
		if row.Origin != string(model.AIJobOriginDynamic) {
			updates["origin_traceparent"] = originTraceparent(tx.Statement.Context)
			updates["origin_tracestate"] = originTracestate(tx.Statement.Context)
		}
		res := tx.Model(&aiJobRow{}).Where("id = ? AND state IN ?", id, []model.AIJobState{model.AIJobFailed, model.AIJobCanceled, model.AIJobSkipped}).Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrAIJobNotRetryable
		}
		return tx.Model(&aiJobRow{}).Where("depends_on_job_id = ? AND state = ?", id, model.AIJobSkipped).Updates(map[string]any{
			"state": model.AIJobQueued, "stage": "queued", "progress": 0, "error_code": "", "last_error": "", "finished_at": nil, "updated_at": now,
		}).Error
	})
}

func (s *Store) DeleteAIJob(id string) error {
	res := s.db.Where("id = ? AND state IN ? AND NOT EXISTS (SELECT 1 FROM ai_jobs dependency WHERE dependency.depends_on_job_id = ai_jobs.id)", id, []model.AIJobState{model.AIJobSucceeded, model.AIJobFailed, model.AIJobCanceled, model.AIJobSkipped}).Delete(&aiJobRow{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrAIJobNotTerminal
	}
	return nil
}

func (s *Store) enqueueAINotificationTx(tx *gorm.DB, row aiJobRow, result any, succeeded bool, code, message string) error {
	if row.Origin != string(model.AIJobOriginDynamic) || row.SourceDynamicID == "" {
		return nil
	}
	var channels []string
	if err := json.Unmarshal([]byte(row.TargetChannelIDs), &channels); err != nil {
		return err
	}
	var dynamicRow dynamicRow
	if err := tx.Where("id = ?", row.SourceDynamicID).Take(&dynamicRow).Error; err != nil {
		return err
	}
	var dynamic model.Dynamic
	if err := json.Unmarshal([]byte(dynamicRow.PayloadJSON), &dynamic); err != nil {
		return err
	}
	body, title := "", dynamic.Title
	switch value := result.(type) {
	case model.AITranscriptionResult:
		body, title = transcriptionNotificationText(value), value.Title
	case model.AISummaryResult:
		body = value.Markdown
	}
	notification := &model.AINotification{
		JobID: row.ID, DynamicID: row.SourceDynamicID, BVID: dynamic.BVID, UPName: dynamic.UPName, Title: title,
		Stage: model.AIJobKind(row.Kind), Succeeded: succeeded, Body: body, ErrorCode: code, ErrorMessage: message,
		SourceURL: dynamic.TargetURL,
	}
	if notification.SourceURL == "" {
		notification.SourceURL = dynamic.URL
	}
	now := time.Now()
	for _, channelID := range channels {
		delivery := model.Delivery{
			ID: fmt.Sprintf("ai:%s:%d:%s", row.ID, row.Attempts, channelID), Kind: model.DeliveryKindAI, AI: notification,
			ChannelID: channelID, State: model.DeliveryPending, NextAt: now, CreatedAt: now,
			OriginTraceparent: originTraceparent(tx.Statement.Context),
		}
		if err := putDeliveryTx(tx, delivery); err != nil {
			return err
		}
	}
	return nil
}

func transcriptionNotificationText(result model.AITranscriptionResult) string {
	var body strings.Builder
	for _, page := range result.Pages {
		if len(result.Pages) > 1 {
			fmt.Fprintf(&body, "P%d %s\n", page.Page, page.Title)
		}
		for _, segment := range page.Segments {
			minutes, seconds := segment.StartMS/60000, (segment.StartMS/1000)%60
			fmt.Fprintf(&body, "[%02d:%02d] %s\n", minutes, seconds, segment.Text)
		}
	}
	return strings.TrimSpace(body.String())
}

// AIJobsForDynamic returns the automatic pipeline in execution order.
func (s *Store) AIJobsForDynamic(dynamicID string, detail bool) ([]model.AIJob, error) {
	var rows []aiJobRow
	if err := s.db.Where("source_dynamic_id = ?", dynamicID).
		Order("CASE kind WHEN 'transcription' THEN 0 ELSE 1 END, created_at, id").Find(&rows).Error; err != nil {
		return nil, err
	}
	jobs := make([]model.AIJob, 0, len(rows))
	for _, row := range rows {
		job, err := s.aiJob(row, detail)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (s *Store) InterruptRunningAIJobs() (int64, error) {
	now := time.Now().Unix()
	res := s.db.Model(&aiJobRow{}).Where("state = ?", model.AIJobRunning).Updates(map[string]any{
		"state": model.AIJobFailed, "stage": "failed", "error_code": "worker_interrupted", "last_error": "the process stopped while the AI job was running", "finished_at": now, "updated_at": now,
	})
	return res.RowsAffected, res.Error
}
