package service

import (
	"github.com/jkuri/abstruse/internal/server/app"
	"github.com/jkuri/abstruse/internal/server/db/model"
	"github.com/jkuri/abstruse/internal/server/db/repository"
)

// BuildService interface
type BuildService interface {
	TriggerBuild(repoID, userID uint) bool
	StopBuild(id uint) bool
	RestartBuild(id uint) bool
	Find(id uint) (model.Build, error)
	FindAll(id uint) (model.Build, error)
	FindBuilds(limit, offset int) ([]model.Build, error)
	FindByRepoID(repoID uint, limit, offset int) ([]model.Build, error)
	FindJob(id uint) (*model.Job, error)
}

// DefaultBuildService comment
type DefaultBuildService struct {
	repo    repository.BuildRepository
	jobRepo repository.JobRepository
	app     *app.App
}

// NewBuildService returns new instance of BuildService
func NewBuildService(repo repository.BuildRepository, jobRepo repository.JobRepository, app *app.App) BuildService {
	return &DefaultBuildService{repo, jobRepo, app}
}

// TriggerBuild triggers build for repository.
func (s *DefaultBuildService) TriggerBuild(repoID, userID uint) bool {
	if err := s.app.TriggerBuild(repoID, userID); err != nil {
		return false
	}
	return true
}

// StopBuild stops the build and related jobs.
func (s *DefaultBuildService) StopBuild(id uint) bool {
	if _, err := s.app.StopBuild(id); err != nil {
		return false
	}
	return true
}

// RestartBuild restarts the build.
func (s *DefaultBuildService) RestartBuild(id uint) bool {
	if err := s.app.RestartBuild(id); err != nil {
		return false
	}
	return true
}

// Find finds build by id.
func (s *DefaultBuildService) Find(id uint) (model.Build, error) {
	return s.repo.Find(id)
}

// FindAll finds build by id with preloaded jobs and repository data.
func (s *DefaultBuildService) FindAll(id uint) (model.Build, error) {
	return s.repo.FindAll(id)
}

// FindBuilds finds builds with preloaded jobs and repo data.
func (s *DefaultBuildService) FindBuilds(limit, offset int) ([]model.Build, error) {
	return s.repo.FindBuilds(limit, offset)
}

// FindByRepoID finds builds by repo id with preloaded jobs and repo data.
func (s *DefaultBuildService) FindByRepoID(repoID uint, limit, offset int) ([]model.Build, error) {
	return s.repo.FindByRepoID(repoID, limit, offset)
}

// FindJob finds job by id.
func (s *DefaultBuildService) FindJob(id uint) (*model.Job, error) {
	return s.jobRepo.Find(id)
}
