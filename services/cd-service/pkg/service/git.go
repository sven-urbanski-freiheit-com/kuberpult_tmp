/*This file is part of kuberpult.

Kuberpult is free software: you can redistribute it and/or modify
it under the terms of the Expat(MIT) License as published by
the Free Software Foundation.

Kuberpult is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
MIT License for more details.

You should have received a copy of the MIT License
along with kuberpult. If not, see <https://directory.fsf.org/wiki/License:Expat>.

Copyright 2023 freiheit.com*/

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	api "github.com/freiheit-com/kuberpult/pkg/api/v1"
	grpcErrors "github.com/freiheit-com/kuberpult/pkg/grpc"
	"github.com/freiheit-com/kuberpult/pkg/logger"
	"github.com/freiheit-com/kuberpult/pkg/uuid"
	"github.com/freiheit-com/kuberpult/pkg/valid"
	eventmod "github.com/freiheit-com/kuberpult/services/cd-service/pkg/event"
	"github.com/freiheit-com/kuberpult/services/cd-service/pkg/repository"
	billy "github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/util"
	"github.com/onokonem/sillyQueueServer/timeuuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type GitServer struct {
	Config          repository.RepositoryConfig
	OverviewService *OverviewServiceServer
}

func (s *GitServer) GetGitTags(ctx context.Context, in *api.GetGitTagsRequest) (*api.GetGitTagsResponse, error) {
	tags, err := repository.GetTags(s.Config, "./repository_tags", ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get tags from repository: %v", err)
	}

	return &api.GetGitTagsResponse{TagData: tags}, nil
}

func (s *GitServer) GetProductSummary(ctx context.Context, in *api.GetProductSummaryRequest) (*api.GetProductSummaryResponse, error) {
	if in.Environment == nil && in.EnvironmentGroup == nil {
		return nil, fmt.Errorf("Must have an environment or environmentGroup to get the product summary for")
	}
	if in.Environment != nil && in.EnvironmentGroup != nil {
		if *in.Environment != "" && *in.EnvironmentGroup != "" {
			return nil, fmt.Errorf("Can not have both an environment and environmentGroup to get the product summary for")
		}
	}
	if in.CommitHash == "" {
		return nil, fmt.Errorf("Must have a commit to get the product summary for")
	}
	response, err := s.OverviewService.GetOverview(ctx, &api.GetOverviewRequest{GitRevision: in.CommitHash})
	if err != nil {
		return nil, fmt.Errorf("unable to get overview for %s: %v", in.CommitHash, err)
	}

	var summaryFromEnv []api.ProductSummary
	if in.Environment != nil && *in.Environment != "" {
		for _, group := range response.EnvironmentGroups {
			for _, env := range group.Environments {
				if env.Name == *in.Environment {
					for _, app := range env.Applications {
						summaryFromEnv = append(summaryFromEnv, api.ProductSummary{App: app.Name, Version: strconv.FormatUint(app.Version, 10), Environment: *in.Environment})
					}
				}
			}
		}
		if len(summaryFromEnv) == 0 {
			return &api.GetProductSummaryResponse{}, nil
		}
		sort.Slice(summaryFromEnv, func(i, j int) bool {
			a := summaryFromEnv[i].App
			b := summaryFromEnv[j].App
			return a < b
		})
	} else {
		for _, group := range response.EnvironmentGroups {
			if *in.EnvironmentGroup == group.EnvironmentGroupName {
				for _, env := range group.Environments {
					var singleEnvSummary []api.ProductSummary
					for _, app := range env.Applications {
						singleEnvSummary = append(singleEnvSummary, api.ProductSummary{App: app.Name, Version: strconv.FormatUint(app.Version, 10), Environment: env.Name})
					}
					sort.Slice(singleEnvSummary, func(i, j int) bool {
						a := singleEnvSummary[i].App
						b := singleEnvSummary[j].App
						return a < b
					})
					summaryFromEnv = append(summaryFromEnv, singleEnvSummary...)
				}
			}
		}
		if len(summaryFromEnv) == 0 {
			return nil, nil
		}
	}

	var productVersion []*api.ProductSummary
	for _, row := range summaryFromEnv {
		for _, app := range response.Applications {
			if row.App == app.Name {
				for _, release := range app.Releases {
					if strconv.FormatUint(release.Version, 10) == row.Version {
						productVersion = append(productVersion, &api.ProductSummary{App: row.App, Version: row.Version, CommitId: release.SourceCommitId, DisplayVersion: release.DisplayVersion, Environment: row.Environment, Team: app.Team})
						break
					}
				}
			}
		}
	}
	return &api.GetProductSummaryResponse{ProductSummary: productVersion}, nil
}

func (s *GitServer) GetCommitInfo(ctx context.Context, in *api.GetCommitInfoRequest) (*api.GetCommitInfoResponse, error) {
	if !s.Config.WriteCommitData {
		return nil, status.Error(codes.FailedPrecondition, "no written commit info available; set KUBERPULT_GIT_WRITE_COMMIT_DATA=true to enable")
	}

	fs := s.OverviewService.Repository.State().Filesystem

	commitIDPrefix := in.CommitHash

	commitID, err := findCommitID(ctx, fs, commitIDPrefix)
	if err != nil {
		return nil, err
	}

	commitPath := fs.Join("commits", commitID[:2], commitID[2:])

	sourceMessagePath := fs.Join(commitPath, "source_message")
	var commitMessage string
	if dat, err := util.ReadFile(fs, sourceMessagePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, status.Error(codes.NotFound, "commit info does not exist")
		}
		return nil, fmt.Errorf("could not open the source message file at %s, err: %w", sourceMessagePath, err)
	} else {
		commitMessage = string(dat)
	}

	commitApplicationsDirPath := fs.Join(commitPath, "applications")
	dirs, err := fs.ReadDir(commitApplicationsDirPath)
	if err != nil {
		return nil, fmt.Errorf("could not read the applications directory at %s, error: %w", commitApplicationsDirPath, err)
	}
	touchedApps := make([]string, 0)
	for _, dir := range dirs {
		touchedApps = append(touchedApps, dir.Name())
	}
	sort.Strings(touchedApps)

	events, err := s.GetEvents(ctx, fs, commitPath)
	if err != nil {
		return nil, err
	}

	return &api.GetCommitInfoResponse{
		CommitHash:    commitID,
		CommitMessage: commitMessage,
		TouchedApps:   touchedApps,
		Events:        events,
	}, nil
}

func (s *GitServer) GetEvents(ctx context.Context, fs billy.Filesystem, commitPath string) ([]*api.Event, error) {
	var result []*api.Event
	allEventsPath := fs.Join(commitPath, "events")
	potentialEventDirs, err := fs.ReadDir(allEventsPath)
	if err != nil {
		return nil, fmt.Errorf("could not read events directory '%s': %v", allEventsPath, err)
	}
	for i := range potentialEventDirs {
		oneEventDir := potentialEventDirs[i]
		if oneEventDir.IsDir() {
			fileName := oneEventDir.Name()
			rawUUID, err := timeuuid.ParseUUID(fileName)
			if err != nil {
				return nil, fmt.Errorf("could not read event directory '%s' not a UUID: %v", fs.Join(allEventsPath, fileName), err)
			}

			var event *api.Event
			event, err = s.ReadEvent(ctx, fs, fs.Join(allEventsPath, fileName), rawUUID)
			if err != nil {
				return nil, fmt.Errorf("could not read events %v", err)
			}
			result = append(result, event)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.AsTime().UnixNano() < result[j].CreatedAt.AsTime().UnixNano()
	})
	return result, nil
}

func (s *GitServer) ReadEvent(ctx context.Context, fs billy.Filesystem, eventPath string, eventId timeuuid.UUID) (*api.Event, error) {
	eventTypePath := fs.Join(eventPath, "eventType")
	eventTypeRaw, err := util.ReadFile(fs, eventTypePath) // full path: commits/<commitHash2>/<commitHash38>/events/<eventUUID>/eventType
	if err != nil {
		return nil, fmt.Errorf("could not read eventType in path %s - %v", eventTypePath, err)
	}
	var eventType = string(eventTypeRaw)

	if eventType == eventmod.NewReleaseEventName {
		eventEnvsPath := fs.Join(eventPath, "environments")

		potentialEnvironmentDirs, err := fs.ReadDir(eventEnvsPath)
		if err != nil {
			return nil, fmt.Errorf("could not read events environments directory '%s' - %v", eventEnvsPath, err)
		}

		var envs []string = nil
		for i := range potentialEnvironmentDirs {
			envDir := potentialEnvironmentDirs[i]
			fileName := envDir.Name()
			if envDir.IsDir() {
				envs = append(envs, fileName)
			} else {
				logger.FromContext(ctx).Sugar().Warnf(
					"found entry in %s that was not an environment directory", fs.Join(eventEnvsPath, fileName))
			}
		}

		var result = &api.Event{
			CreatedAt: uuid.GetTime(&eventId),
			EventType: &api.Event_CreateReleaseEvent{
				CreateReleaseEvent: &api.CreateReleaseEvent{
					EnvironmentNames: envs,
				},
			},
		}
		return result, nil
	}
	return nil, fmt.Errorf("could not read event, did not recognize event type '%s'", eventType)
}

// findCommitID checks if the "commits" directory in the given
// filesystem contains a commit with the given prefix. Returns the
// full hash of the commit, if a unique one can be found. Returns a
// gRPC error that can be directly returned to the client.
func findCommitID(
	ctx context.Context,
	fs billy.Filesystem,
	commitPrefix string,
) (string, error) {
	if !valid.SHA1CommitIDPrefix(commitPrefix) {
		return "", status.Error(codes.InvalidArgument,
			"not a valid commit_hash")
	}
	commitPrefix = strings.ToLower(commitPrefix)
	if len(commitPrefix) == valid.SHA1CommitIDLength {
		// the easy case: the commit has been requested in
		// full length, so we simply check if the file exist
		// and are done.
		commitPath := fs.Join("commits", commitPrefix[:2], commitPrefix[2:])

		if _, err := fs.Stat(commitPath); err != nil {
			return "", grpcErrors.NotFoundError(ctx,
				fmt.Errorf("commit %s was not found in the manifest repo", commitPrefix))
		}

		return commitPrefix, nil
	}
	if len(commitPrefix) < 7 {
		return "", status.Error(codes.InvalidArgument,
			"commit_hash too short (must be at least 7 characters)")
	}
	// the dir we're looking in
	commitDir := fs.Join("commits", commitPrefix[:2])
	files, err := fs.ReadDir(commitDir)
	if err != nil {
		return "", grpcErrors.NotFoundError(ctx,
			fmt.Errorf("commit with prefix %s was not found in the manifest repo", commitPrefix))
	}
	// the prefix of the file we're looking for
	filePrefix := commitPrefix[2:]
	var commitID string
	for _, file := range files {
		fileName := file.Name()
		if !strings.HasPrefix(fileName, filePrefix) {
			continue
		}
		if commitID != "" {
			// another commit has already been found
			return "", status.Error(codes.InvalidArgument,
				"commit_hash is not unique, provide the complete hash (or a longer prefix)")
		}
		commitID = commitPrefix[:2] + fileName
	}
	if commitID == "" {
		return "", grpcErrors.NotFoundError(ctx,
			fmt.Errorf("commit with prefix %s was not found in the manifest repo", commitPrefix))
	}
	return commitID, nil
}
