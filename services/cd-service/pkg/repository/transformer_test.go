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

package repository

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os/exec"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/freiheit-com/kuberpult/services/cd-service/pkg/repository/testutil"

	"github.com/DataDog/datadog-go/v5/statsd"
	"github.com/google/go-cmp/cmp/cmpopts"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"

	api "github.com/freiheit-com/kuberpult/pkg/api/v1"
	"github.com/freiheit-com/kuberpult/pkg/auth"
	"github.com/freiheit-com/kuberpult/pkg/ptr"
	"github.com/freiheit-com/kuberpult/pkg/testfs"
	"github.com/freiheit-com/kuberpult/pkg/valid"
	"github.com/freiheit-com/kuberpult/services/cd-service/pkg/config"
	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/util"
	"github.com/google/go-cmp/cmp"

	godebug "github.com/kylelemons/godebug/diff"
)

const (
	envAcceptance      = "acceptance"
	envProduction      = "production"
	additionalVersions = 7
)

var timeNowOld = time.Date(1999, 01, 02, 03, 04, 05, 0, time.UTC)

func TestUndeployApplicationErrors(t *testing.T) {
	tcs := []struct {
		Name              string
		Transformers      []Transformer
		expectedError     string
		expectedCommitMsg string
		shouldSucceed     bool
	}{
		{
			Name: "Delete non-existent application",
			Transformers: []Transformer{
				&UndeployApplication{
					Application: "app1",
				},
			},
			expectedError:     "UndeployApplication: error cannot undeploy non-existing application 'app1'",
			expectedCommitMsg: "",
			shouldSucceed:     false,
		},
		{
			Name: "Success",
			Transformers: []Transformer{
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envProduction: "productionmanifest",
					},
					WriteCommitData: true,
				},
				&CreateUndeployApplicationVersion{
					Application: "app1",
				},
				&UndeployApplication{
					Application: "app1",
				},
			},
			expectedError:     "",
			expectedCommitMsg: "application 'app1' was deleted successfully",
			shouldSucceed:     true,
		},
		{
			Name: "Create un-deploy Version for un-deployed application should not work",
			Transformers: []Transformer{
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envProduction: "productionmanifest",
					},
					WriteCommitData: true,
				},
				&CreateUndeployApplicationVersion{
					Application: "app1",
				},
				&UndeployApplication{
					Application: "app1",
				},
				&CreateUndeployApplicationVersion{
					Application: "app1",
				},
			},
			expectedError:     "cannot undeploy non-existing application 'app1'",
			expectedCommitMsg: "",
			shouldSucceed:     false,
		},
		{
			Name: "Undeploy application where there is an application lock should not work",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "acceptance",
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: envAcceptance, Latest: true}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envAcceptance: "acceptance",
					},
					WriteCommitData: true,
				},
				&CreateUndeployApplicationVersion{
					Application: "app1",
				},
				&CreateEnvironmentApplicationLock{
					Environment: "acceptance",
					Application: "app1",
					LockId:      "22133",
					Message:     "test",
				},
				&UndeployApplication{
					Application: "app1",
				},
			},
			expectedError:     "",
			expectedCommitMsg: "application 'app1' was deleted successfully",
			shouldSucceed:     true,
		},
		{
			Name: "Undeploy application where there is an application lock created after the un-deploy version creation should",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "acceptance",
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: envAcceptance, Latest: true}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envAcceptance: "acceptance",
					},
					WriteCommitData: true,
				},
				&CreateUndeployApplicationVersion{
					Application: "app1",
				},
				&CreateEnvironmentApplicationLock{
					Environment: "acceptance",
					Application: "app1",
					LockId:      "22133",
					Message:     "test",
				},
				&UndeployApplication{
					Application: "app1",
				},
			},
			expectedError:     "",
			expectedCommitMsg: "application 'app1' was deleted successfully",
			shouldSucceed:     true,
		},
		{
			Name: "Undeploy application where there current releases are not undeploy shouldn't work",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "acceptance",
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: envAcceptance, Latest: true}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envAcceptance: "acceptance",
					},
					WriteCommitData: true,
				},
				&CreateEnvironmentLock{
					Environment: "acceptance",
					LockId:      "22133",
					Message:     "test",
				},
				&CreateUndeployApplicationVersion{
					Application: "app1",
				},
				&UndeployApplication{
					Application: "app1",
				},
			},
			expectedError:     "UndeployApplication: error cannot un-deploy application 'app1' the release 'acceptance' is not un-deployed: 'environments/acceptance/applications/app1/version/undeploy'",
			expectedCommitMsg: "",
			shouldSucceed:     false,
		},
		{
			Name: "Undeploy application where the app does not have a release in all envs must work",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "acceptance",
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Latest: true}},
				},
				&CreateEnvironment{
					Environment: "production",
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: envAcceptance, Latest: false}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envAcceptance: "acceptance",
					},
					WriteCommitData: true,
				},
				&CreateUndeployApplicationVersion{
					Application: "app1",
				},
				&UndeployApplication{
					Application: "app1",
				},
			},
			expectedError:     "",
			expectedCommitMsg: "application 'app1' was deleted successfully",
			shouldSucceed:     true,
		},
		{
			Name: "Undeploy application where there is an environment lock should work",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "acceptance",
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: envAcceptance, Latest: true}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envAcceptance: "acceptance",
					},
					WriteCommitData: true,
				},
				&CreateUndeployApplicationVersion{
					Application: "app1",
				},
				&CreateEnvironmentLock{
					Environment: "acceptance",
					LockId:      "22133",
					Message:     "test",
				},
				&UndeployApplication{
					Application: "app1",
				},
			},
			expectedError:     "",
			expectedCommitMsg: "application 'app1' was deleted successfully",
			shouldSucceed:     true,
		},
		{
			Name: "Undeploy application where the last release is not Undeploy shouldn't work",
			Transformers: []Transformer{
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envProduction: "productionmanifest",
					},
					WriteCommitData: true,
				},
				&CreateUndeployApplicationVersion{
					Application: "app1",
				},
				&CreateApplicationVersion{
					Application:     "app1",
					Manifests:       nil,
					SourceCommitId:  "",
					SourceAuthor:    "",
					SourceMessage:   "",
					WriteCommitData: true,
				},
				&UndeployApplication{
					Application: "app1",
				},
			},
			expectedError:     "UndeployApplication: error last release is not un-deployed application version of 'app1'",
			expectedCommitMsg: "",
			shouldSucceed:     false,
		},
	}
	for _, tc := range tcs {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			repo := setupRepositoryTest(t)
			commitMsg, _, _, err := repo.ApplyTransformersInternal(testutil.MakeTestContext(), tc.Transformers...)
			// note that we only check the LAST error here:
			if tc.shouldSucceed {
				if err != nil {
					t.Fatalf("Expected no error: %v", err)
				}
				actualMsg := commitMsg[len(commitMsg)-1]
				if actualMsg != tc.expectedCommitMsg {
					t.Fatalf("expected a different message.\nExpected: %q\nGot %q", tc.expectedCommitMsg, actualMsg)
				}
			} else {
				if err == nil {
					t.Fatalf("Expected an error but got none")
				} else {
					actualMsg := err.Error()
					if actualMsg != tc.expectedError {
						t.Fatalf("expected a different error.\nExpected: %q\nGot %q", tc.expectedError, actualMsg)
					}
				}
			}
		})
	}
}

func TestCreateUndeployApplicationVersionErrors(t *testing.T) {
	tcs := []struct {
		Name             string
		Transformers     []Transformer
		expectedError    string
		expectedPath     string
		shouldSucceed    bool
		expectedFileData []byte
	}{
		{
			Name: "successfully undeploy - should work",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "acceptance",
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: envAcceptance, Latest: true}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envAcceptance: "acceptance",
					},
					WriteCommitData: true,
				},
				&CreateUndeployApplicationVersion{
					Application: "app1",
				},
			},
			expectedError:    "",
			expectedPath:     "applications/app1/releases/2/environments/acceptance/manifests.yaml",
			expectedFileData: []byte(" "),
			shouldSucceed:    true,
		},
		{
			Name: "Does not undeploy - should not succeed",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "acceptance",
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: envAcceptance, Latest: true}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envAcceptance: "acceptance",
					},
					WriteCommitData: true,
				},
			},
			expectedError:    "file does not exist",
			expectedPath:     "",
			expectedFileData: []byte(""),
			shouldSucceed:    false,
		},
	}
	for _, tc := range tcs {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			repo := setupRepositoryTest(t)
			_, updatedState, _, _ := repo.ApplyTransformersInternal(testutil.MakeTestContext(), tc.Transformers...)

			fileData, err := util.ReadFile(updatedState.Filesystem, updatedState.Filesystem.Join(updatedState.Filesystem.Root(), tc.expectedPath))

			if tc.shouldSucceed {
				if err != nil {
					t.Fatalf("Expected no error: %v", err)
				}
				if !cmp.Equal(fileData, tc.expectedFileData) {
					t.Fatalf("Expected %v, got %v", tc.expectedFileData, fileData)
				}
			} else {
				if err == nil {
					t.Fatal("Expected error but got none")
				} else {
					actualMsg := err.Error()
					if actualMsg != tc.expectedError {
						t.Fatalf("expected a different error.\nExpected: %q\nGot %q", tc.expectedError, actualMsg)
					}
				}
			}
		})
	}
}

func TestCreateApplicationVersionEvents(t *testing.T) {
	fakeGen := testutil.TestGenerator{
		Time: timeNowOld,
	}

	tcs := []struct {
		Name          string
		Transformers  []Transformer
		expectedError string
		expectedPaths []string
	}{
		{
			Name: "createRelease event should write files",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "acceptance",
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: envAcceptance, Latest: false}},
				},
				&CreateApplicationVersion{
					Authentication: Authentication{},
					Version:        42,
					Application:    "app1",
					Manifests: map[string]string{
						envAcceptance: envAcceptance,
						envProduction: envProduction,
					},
					SourceCommitId:  "cafe1cafe2cafe1cafe2cafe1cafe2cafe1cafe2",
					SourceAuthor:    "best Author",
					SourceMessage:   "smart message",
					SourceRepoUrl:   "",
					Team:            "",
					DisplayVersion:  "",
					WriteCommitData: true,
				},
			},
			expectedError: "",
			expectedPaths: []string{
				"environments/acceptance/.gitkeep",
				"environments/production/.gitkeep",
				"eventType",
			},
		},
	}
	for _, tc := range tcs {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			repo := setupRepositoryTest(t)
			ctx := testutil.MakeTestContext()
			ctx = AddGeneratorToContext(ctx, fakeGen)
			_, updatedState, _, err := repo.ApplyTransformersInternal(ctx, tc.Transformers...)
			if err != nil {
				t.Fatalf("expected no error but transformer failed with %v", err)
			}
			// find out the name of the events directory:
			baseDir := "commits/ca/fe1cafe2cafe1cafe2cafe1cafe2cafe1cafe2/events/"
			fs := updatedState.Filesystem
			files, err := fs.ReadDir(baseDir)
			if len(files) != 1 {
				t.Fatalf("Expected one event: %s - bot got %d", baseDir, len(files))
			}
			file := files[0]
			eventId := file.Name()

			for i := range tc.expectedPaths {
				expectedPath := tc.expectedPaths[i]
				expectedFullPath := fs.Join(baseDir, eventId, expectedPath)
				filename := updatedState.Filesystem.Join(updatedState.Filesystem.Root(), expectedFullPath)
				_, err := util.ReadFile(updatedState.Filesystem, filename)
				if err != nil {
					t.Fatalf("Expected no error: %v - file issue %s", err, filename)
				}
			}
		})
	}
}

func TestDeployOnSelectedEnvs(t *testing.T) {
	type Expected struct {
		Path     string
		fileData *string
	}
	tcs := []struct {
		Name         string
		Transformers []Transformer
		Expected     []Expected
	}{
		{
			Name: "generates multiple manifests",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: envAcceptance,
					Config:      testutil.MakeEnvConfigLatest(&config.EnvironmentConfigArgoCd{}),
				},
				&CreateEnvironment{
					Environment: envProduction,
					Config:      testutil.MakeEnvConfigLatest(&config.EnvironmentConfigArgoCd{}),
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envAcceptance: "acc1",
						envProduction: "prod1",
					},
					WriteCommitData: true,
				},
			},
			Expected: []Expected{
				{
					Path: "argocd/v1alpha1/acceptance.yaml",
					fileData: ptr.FromString(`apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: acceptance
spec:
  description: acceptance
  destinations:
  - {}
  sourceRepos:
  - '*'
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  annotations:
    argocd.argoproj.io/manifest-generate-paths: /environments/acceptance/applications/app1/manifests
    com.freiheit.kuberpult/application: app1
    com.freiheit.kuberpult/environment: acceptance
    com.freiheit.kuberpult/team: ""
  finalizers:
  - resources-finalizer.argocd.argoproj.io
  labels:
    com.freiheit.kuberpult/team: ""
  name: acceptance-app1
spec:
  destination: {}
  project: acceptance
  source:
    path: environments/acceptance/applications/app1/manifests
    repoURL: %%%REPO%%%
    targetRevision: master
  syncPolicy:
    automated:
      allowEmpty: true
      prune: true
      selfHeal: true
`),
				},
				{
					Path: "argocd/v1alpha1/production.yaml",
					fileData: ptr.FromString(`apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: production
spec:
  description: production
  destinations:
  - {}
  sourceRepos:
  - '*'
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  annotations:
    argocd.argoproj.io/manifest-generate-paths: /environments/production/applications/app1/manifests
    com.freiheit.kuberpult/application: app1
    com.freiheit.kuberpult/environment: production
    com.freiheit.kuberpult/team: ""
  finalizers:
  - resources-finalizer.argocd.argoproj.io
  labels:
    com.freiheit.kuberpult/team: ""
  name: production-app1
spec:
  destination: {}
  project: production
  source:
    path: environments/production/applications/app1/manifests
    repoURL: %%%REPO%%%
    targetRevision: master
  syncPolicy:
    automated:
      allowEmpty: true
      prune: true
      selfHeal: true
`),
				},
			},
		},
		{
			Name: "generates only deployed manifest",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: envAcceptance,
					Config:      testutil.MakeEnvConfigLatest(&config.EnvironmentConfigArgoCd{}),
				},
				&CreateEnvironment{
					Environment: envProduction,
					Config:      testutil.MakeEnvConfigUpstream(envAcceptance, &config.EnvironmentConfigArgoCd{}),
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envAcceptance: "acc2",
						envProduction: "prod2",
					},
					WriteCommitData: true,
				},
			},
			Expected: []Expected{
				{
					Path: "argocd/v1alpha1/acceptance.yaml",
					fileData: ptr.FromString(`apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: acceptance
spec:
  description: acceptance
  destinations:
  - {}
  sourceRepos:
  - '*'
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  annotations:
    argocd.argoproj.io/manifest-generate-paths: /environments/acceptance/applications/app1/manifests
    com.freiheit.kuberpult/application: app1
    com.freiheit.kuberpult/environment: acceptance
    com.freiheit.kuberpult/team: ""
  finalizers:
  - resources-finalizer.argocd.argoproj.io
  labels:
    com.freiheit.kuberpult/team: ""
  name: acceptance-app1
spec:
  destination: {}
  project: acceptance
  source:
    path: environments/acceptance/applications/app1/manifests
    repoURL: %%%REPO%%%
    targetRevision: master
  syncPolicy:
    automated:
      allowEmpty: true
      prune: true
      selfHeal: true
`),
				},
				{
					Path: "argocd/v1alpha1/production.yaml",
					// here we expect only the appProject with the app, because it hasn't been deployed yet
					fileData: ptr.FromString(`apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: production
spec:
  description: production
  destinations:
  - {}
  sourceRepos:
  - '*'
`),
				},
			},
		},
	}
	for _, tc := range tcs {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			repo, repoUrl := setupRepositoryTestWithPath(t)

			err := repo.Apply(testutil.MakeTestContext(), tc.Transformers...)
			if err != nil {
				t.Fatalf("Unexpected error ApplyTransformersInternal: %v", err)
			}
			for i, expected := range tc.Expected {
				fileData, err := util.ReadFile(repo.State().Filesystem, repo.State().Filesystem.Join(repo.State().Filesystem.Root(), expected.Path))
				if err == nil {
					if expected.fileData == nil {
						t.Fatalf("Expected [%d] an error but got content: '%s'", i, string(fileData))
					}
					var actual = string(fileData)
					var exp = strings.ReplaceAll(ptr.ToString(expected.fileData), "%%%REPO%%%", repoUrl)
					if diff := cmp.Diff(actual, exp); diff != "" {
						t.Errorf("got %v, want %v, diff (-want +got) %s", actual, exp, diff)
					}
				} else {
					// there is an error
					if expected.fileData != nil {
						t.Fatalf("Expected [%d] file data '%s' but got error: %v", i, ptr.ToString(expected.fileData), err)
					}
				}
			}
		})
	}
}

func TestCreateApplicationVersionIdempotency(t *testing.T) {
	tcs := []struct {
		Name             string
		Transformers     []Transformer
		expectedErrorMsg string
	}{
		{
			Name: "recreate same version with idempotence",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "acceptance",
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: envAcceptance, Latest: false}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Version:     10000,
					Manifests: map[string]string{
						envAcceptance: "{}",
					},
					WriteCommitData: true,
				},
				&CreateApplicationVersion{
					Application: "app1",
					Version:     10000,
					Manifests: map[string]string{
						envAcceptance: "{}",
					},
					WriteCommitData: true,
				},
			},
			expectedErrorMsg: "already_exists_same:{}",
		},
		{
			Name: "recreate same version without idempotence",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "acceptance",
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: envAcceptance, Latest: false}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Version:     10000,
					Manifests: map[string]string{
						envAcceptance: `{}`,
					},
					WriteCommitData: true,
				},
				&CreateApplicationVersion{
					Application: "app1",
					Version:     10000,
					Manifests: map[string]string{
						envAcceptance: `{ "different": "yes" }`,
					},
					WriteCommitData: true,
				},
			},
			expectedErrorMsg: `already_exists_different:{first_differing_field:MANIFESTS diff:"--- acceptance-existing\n+++ acceptance-request\n@@ -1 +1 @@\n-{}\n\\ No newline at end of file\n+{ \"different\": \"yes\" }\n\\ No newline at end of file\n"}`,
		},
		{
			Name: "recreate same version with idempotence, but different formatting of yaml",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "acceptance",
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: envAcceptance, Latest: false}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Version:     10000,
					Manifests: map[string]string{
						envAcceptance: `{ "different":                  "yes" }`,
					},
					WriteCommitData: true,
				},
				&CreateApplicationVersion{
					Application: "app1",
					Version:     10000,
					Manifests: map[string]string{
						envAcceptance: `{ "different": "yes" }`,
					},
					WriteCommitData: true,
				},
			},
			expectedErrorMsg: "already_exists_same:{}",
		},
	}
	for _, tc := range tcs {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			ctxWithTime := WithTimeNow(testutil.MakeTestContext(), timeNowOld)
			t.Parallel()

			// optimization: no need to set up the repository if this fails
			var expectedErr api.CreateReleaseResponse
			if err := prototext.Unmarshal([]byte(tc.expectedErrorMsg), &expectedErr); err != nil {
				t.Fatalf("failed to unmarshal the expected error object: %v", err)
			}
			repo := setupRepositoryTest(t)
			_, _, _, err := repo.ApplyTransformersInternal(ctxWithTime, tc.Transformers...)
			if err == nil {
				t.Fatalf("expected error, got none.")
			}
			var actualErr api.CreateReleaseResponse
			if err := prototext.Unmarshal([]byte(err.Error()), &actualErr); err != nil {
				t.Fatalf("failed to unmarshal the actual error object: %v", err)
			}

			if !proto.Equal(&expectedErr, &actualErr) {
				t.Fatalf("Expected different error (expected: %v, got: %v)", expectedErr, actualErr)
			}
		})
	}
}

func listFilesHelper(fs billy.Filesystem, path string) []string {
	ret := make([]string, 0)

	files, err := fs.ReadDir(path)
	if err == nil {
		for _, file := range files {
			ret = append(ret, listFilesHelper(fs, fs.Join(path, file.Name()))...)
		}
	} else {
		ret = append(ret, path)
	}

	return ret
}

func listFiles(fs billy.Filesystem) []string {
	paths := listFilesHelper(fs, ".")
	sort.Slice(paths, func(i, j int) bool { return paths[i] < paths[j] })
	return paths
}

func verifyCommitPathsExist(fs billy.Filesystem, paths []string) error {
	for _, path := range paths {
		_, err := fs.Stat(path)
		if err != nil {
			return fmt.Errorf(`error verifying commit path exists. path: %s, error: %v
directory tree: %s`, path, err, strings.Join(listFiles(fs), "\n"))
		}
	}
	return nil
}

func verifyCommitPathsDontExist(fs billy.Filesystem, paths []string) error {
	for _, path := range paths {
		_, err := fs.Stat(path)
		if err == nil {
			return fmt.Errorf(`error verifying commit path doesn't exist. path:
%s
error expected but none was raised
directory tree: %s`, path, strings.Join(listFiles(fs), "\n"))
		}
	}
	return nil
}

func verifyConsistency(fs billy.Filesystem) error {
	type ApplicationDirectoryContent struct {
		application    string
		sourceCommitID string
	}
	extractAppCommitPairsFromApplications := func(fs billy.Filesystem) ([]ApplicationDirectoryContent, error) {
		applications := make([]ApplicationDirectoryContent, 0)
		applicationsDir, err := fs.ReadDir("applications")
		if err != nil {
			return nil, fmt.Errorf("could not open the applications directory: %w", err)
		}

		for _, applicationDir := range applicationsDir {
			releasesDir, err := fs.ReadDir(fs.Join("applications", applicationDir.Name(), "releases"))
			if err != nil {
				return nil, fmt.Errorf("could not open the releases directory: %w", err)
			}
			for _, releaseDir := range releasesDir {
				commitIDFile, err := util.ReadFile(fs, fs.Join("applications", applicationDir.Name(), "releases", releaseDir.Name(), "source_commit_id"))

				if err != nil {
					return nil, fmt.Errorf("could not read the source commit ID file: %w", err)
				}

				sourceCommitID := string(commitIDFile)
				if valid.SHA1CommitID(sourceCommitID) {
					applications = append(applications, ApplicationDirectoryContent{
						application:    applicationDir.Name(),
						sourceCommitID: sourceCommitID,
					})
				}
			}
		}
		return applications, nil
	}

	applications, err := extractAppCommitPairsFromApplications(fs)
	if err != nil {
		return fmt.Errorf("unable to extract (application, commit) pairs from applications directory, error: %w", err)
	}

	type CommitDirectoryContent struct {
		application    string
		sourceCommitID string
	}

	extractAppCommitPairsFromCommits := func(fs billy.Filesystem) ([]CommitDirectoryContent, error) {
		commits := make([]CommitDirectoryContent, 0)

		commitsDir1, err := fs.ReadDir("commits")
		if err != nil {
			return nil, fmt.Errorf("could not open the commits directory: %w", err)
		}

		for _, commitDir1 := range commitsDir1 {
			commitsDir2, err := fs.ReadDir(fs.Join("commits", commitDir1.Name()))
			if err != nil {
				return nil, fmt.Errorf("could not open the commit directory 1")
			}

			for _, commitDir2 := range commitsDir2 {
				applicationsDir, err := fs.ReadDir(fs.Join("commits", commitDir1.Name(), commitDir2.Name(), "applications"))
				if err != nil {
					return nil, fmt.Errorf("could not open the applications directory in the commits tree: %w", err)
				}

				for _, applicationDir := range applicationsDir {
					commits = append(commits, CommitDirectoryContent{
						application:    applicationDir.Name(),
						sourceCommitID: commitDir1.Name() + commitDir2.Name(),
					})
				}
			}
		}

		return commits, nil
	}

	commits, err := extractAppCommitPairsFromCommits(fs)
	if err != nil {
		return fmt.Errorf("unable to extract (application, commit) pairs from commits directory, error: %w", err)
	}

	for _, app := range applications {
		commitFound := false
		for _, commit := range commits {
			if app.application == commit.application && app.sourceCommitID == commit.sourceCommitID {
				commitFound = true
			}
		}
		if !commitFound {
			return fmt.Errorf(`an (app, commit) combination was found in the application tree but not in the commits tree:
application tree pairs: %v
commit tree pairs: %v
missing: %v
directory tree: %v`, applications, commits, app, strings.Join(listFiles(fs), "\n"))
		}
	}
	for _, commit := range commits {
		appFound := false
		for _, app := range applications {
			if app.application == commit.application && app.sourceCommitID == commit.sourceCommitID {
				appFound = true
			}
		}
		if !appFound {
			return fmt.Errorf(`an (app, commit) combination was found in the commits tree but not in the applications tree:
application tree pairs: %v
commit tree pairs: %v
missing: %v
directory tree: %v`, applications, commits, commit, strings.Join(listFiles(fs), "\n"))
		}
	}
	return nil
}

func randomCommitID() string {
	commitID := make([]byte, 20)
	rand.Read(commitID)
	return hex.EncodeToString(commitID)
}

func concatenate[T any](slices ...[]T) []T {
	var totalLen int
	for _, s := range slices {
		totalLen += len(s)
	}

	result := make([]T, totalLen)

	var i int
	for _, s := range slices {
		i += copy(result[i:], s)
	}

	return result
}

func TestCreateApplicationVersionCommitPath(t *testing.T) {
	type TestCase struct {
		Name                   string
		Transformers           []Transformer
		ExistentCommitPaths    []string
		NonExistentCommitPaths []string
	}

	intToSHA1 := func(n int) string {
		ret := strconv.Itoa(n)
		ret = strings.Repeat("0", 40-len(ret)) + ret
		return ret
	}

	manyCreateApplication := func(app string, n int) []Transformer {
		ret := make([]Transformer, 0)

		for i := 1; i <= n; i++ {
			ret = append(ret, &CreateApplicationVersion{
				Application:    app,
				SourceCommitId: intToSHA1(i),
				Manifests: map[string]string{
					envAcceptance: "acceptance",
				},
				WriteCommitData: true,
			})
		}
		return ret
	}

	tcs := []TestCase{
		{
			Name: "Create one application with SHA1 commit ID",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "acceptance",
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: envAcceptance, Latest: false}},
				},
				&CreateApplicationVersion{
					Application:    "app",
					SourceCommitId: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					Manifests: map[string]string{
						envAcceptance: "acceptance",
					},
					WriteCommitData: true,
				},
				&DeployApplicationVersion{
					Environment:   envAcceptance,
					Application:   "app",
					Version:       1,
					LockBehaviour: api.LockBehavior_FAIL,
				},
			},
			ExistentCommitPaths: []string{
				"commits/aa/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/applications/app/.gitkeep",
			},
		},
		{
			Name: "Create several applications with different SHA1 commit ID's",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "acceptance",
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: envAcceptance, Latest: false}},
				},
				&CreateApplicationVersion{
					Application:    "app1",
					SourceCommitId: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					Manifests: map[string]string{
						envAcceptance: "acceptance",
					},
					WriteCommitData: true,
				},
				&CreateApplicationVersion{
					Application:    "app2",
					SourceCommitId: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
					Manifests: map[string]string{
						envAcceptance: "acceptance",
					},
					WriteCommitData: true,
				},
				&CreateApplicationVersion{
					Application:    "app3",
					SourceCommitId: "cccccccccccccccccccccccccccccccccccccccc",
					Manifests: map[string]string{
						envAcceptance: "acceptance",
					},
					WriteCommitData: true,
				},
				&DeployApplicationVersion{
					Environment:   envAcceptance,
					Application:   "app1",
					Version:       1,
					LockBehaviour: api.LockBehavior_FAIL,
				},
				&DeployApplicationVersion{
					Environment:   envAcceptance,
					Application:   "app2",
					Version:       1,
					LockBehaviour: api.LockBehavior_FAIL,
				},
				&DeployApplicationVersion{
					Environment:   envAcceptance,
					Application:   "app3",
					Version:       1,
					LockBehaviour: api.LockBehavior_FAIL,
				},
			},
			ExistentCommitPaths: []string{
				"commits/aa/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/applications/app1/.gitkeep",
				"commits/bb/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb/applications/app2/.gitkeep",
				"commits/cc/cccccccccccccccccccccccccccccccccccccc/applications/app3/.gitkeep",
			},
		},
		{
			Name: "Create several applications with different SHA1 commit ID's but the first 2 letters of the commitID's are the same",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "acceptance",
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: envAcceptance, Latest: false}},
				},
				&CreateApplicationVersion{
					Application:    "app1",
					SourceCommitId: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					Manifests: map[string]string{
						envAcceptance: "acceptance",
					},
					WriteCommitData: true,
				},
				&CreateApplicationVersion{
					Application:    "app2",
					SourceCommitId: "aabbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
					Manifests: map[string]string{
						envAcceptance: "acceptance",
					},
					WriteCommitData: true,
				},
				&CreateApplicationVersion{
					Application:    "app3",
					SourceCommitId: "aacccccccccccccccccccccccccccccccccccccc",
					Manifests: map[string]string{
						envAcceptance: "acceptance",
					},
					WriteCommitData: true,
				},
				&DeployApplicationVersion{
					Environment:   envAcceptance,
					Application:   "app1",
					Version:       1,
					LockBehaviour: api.LockBehavior_FAIL,
				},
				&DeployApplicationVersion{
					Environment:   envAcceptance,
					Application:   "app2",
					Version:       1,
					LockBehaviour: api.LockBehavior_FAIL,
				},
				&DeployApplicationVersion{
					Environment:   envAcceptance,
					Application:   "app3",
					Version:       1,
					LockBehaviour: api.LockBehavior_FAIL,
				},
			},
			ExistentCommitPaths: []string{
				"commits/aa/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/applications/app1/.gitkeep",
				"commits/aa/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb/applications/app2/.gitkeep",
				"commits/aa/cccccccccccccccccccccccccccccccccccccc/applications/app3/.gitkeep",
			},
		},
		{
			Name: "Create several applications from the same SHA1 commit ID",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "acceptance",
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: envAcceptance, Latest: false}},
				},
				&CreateApplicationVersion{
					Application:    "app1",
					SourceCommitId: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					Manifests: map[string]string{
						envAcceptance: "acceptance",
					},
					WriteCommitData: true,
				},
				&CreateApplicationVersion{
					Application:    "app2",
					SourceCommitId: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					Manifests: map[string]string{
						envAcceptance: "acceptance",
					},
					WriteCommitData: true,
				},
				&CreateApplicationVersion{
					Application:    "app3",
					SourceCommitId: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					Manifests: map[string]string{
						envAcceptance: "acceptance",
					},
					WriteCommitData: true,
				},
				&DeployApplicationVersion{
					Environment:   envAcceptance,
					Application:   "app1",
					Version:       1,
					LockBehaviour: api.LockBehavior_FAIL,
				},
				&DeployApplicationVersion{
					Environment:   envAcceptance,
					Application:   "app2",
					Version:       1,
					LockBehaviour: api.LockBehavior_FAIL,
				},
				&DeployApplicationVersion{
					Environment:   envAcceptance,
					Application:   "app3",
					Version:       1,
					LockBehaviour: api.LockBehavior_FAIL,
				},
			},
			ExistentCommitPaths: []string{
				"commits/aa/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/applications/app1/.gitkeep",
				"commits/aa/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/applications/app2/.gitkeep",
				"commits/aa/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/applications/app3/.gitkeep",
			},
		},
		{
			Name: "Create application with non SHA1 commit ID",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "acceptance",
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: envAcceptance, Latest: false}},
				},
				&CreateApplicationVersion{
					Application:    "app",
					SourceCommitId: "nonsense",
					Manifests: map[string]string{
						envAcceptance: "acceptance",
					},
					WriteCommitData: true,
				},
				&DeployApplicationVersion{
					Environment:   envAcceptance,
					Application:   "app",
					Version:       1,
					LockBehaviour: api.LockBehavior_FAIL,
				},
			},
			NonExistentCommitPaths: []string{
				"commits/no/nsense/applications/app/.gitkeep",
			},
		},
		{
			Name: "Create application with SHA1 commit ID with uppercase letters",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "acceptance",
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: envAcceptance, Latest: false}},
				},
				&CreateApplicationVersion{
					Application:    "app",
					SourceCommitId: "aaaaaaAAaaaaaaaaaaaaaaaaaaaaaaaaaaAaaaaa",
					Manifests: map[string]string{
						envAcceptance: "acceptance",
					},
					WriteCommitData: true,
				},
				&DeployApplicationVersion{
					Environment:   envAcceptance,
					Application:   "app",
					Version:       1,
					LockBehaviour: api.LockBehavior_FAIL,
				},
			},
			ExistentCommitPaths: []string{
				"commits/aa/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/applications/app/.gitkeep",
			},
			NonExistentCommitPaths: []string{
				"commits/aa/aaaaAAaaaaaaaaaaaaaaaaaaaaaaaaaaAaaaaa/applications/app/.gitkeep",
			},
		},
		{
			Name: "Create the same application many times and deploy the last one",
			Transformers: concatenate(
				[]Transformer{
					&CreateEnvironment{
						Environment: "acceptance",
						Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: envAcceptance, Latest: false}},
					},
				},
				manyCreateApplication("app", 21),
				[]Transformer{
					&DeployApplicationVersion{
						Environment:   envAcceptance,
						Application:   "app",
						Version:       uint64(21),
						LockBehaviour: api.LockBehavior_FAIL,
					},
				},
			),
			ExistentCommitPaths: []string{
				"commits/00/00000000000000000000000000000000000002",
			},
			NonExistentCommitPaths: []string{
				"commits/00/00000000000000000000000000000000000001",
			},
		},
		{
			Name: "Create the same application many times and deploy the last one but with another application in an old commit",
			Transformers: concatenate(
				[]Transformer{
					&CreateEnvironment{
						Environment: "acceptance",
						Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: envAcceptance, Latest: false}},
					},
					&CreateApplicationVersion{
						Application:    "app1",
						SourceCommitId: intToSHA1(1),
						Manifests: map[string]string{
							envAcceptance: "acceptance",
						},
						WriteCommitData: true,
					},
				},
				manyCreateApplication("app2", 21),
				[]Transformer{
					&DeployApplicationVersion{
						Environment:   envAcceptance,
						Application:   "app2",
						Version:       uint64(21),
						LockBehaviour: api.LockBehavior_FAIL,
					},
				},
			),
			ExistentCommitPaths: []string{
				"commits/00/00000000000000000000000000000000000001/applications/app1/.gitkeep",
			},
			NonExistentCommitPaths: []string{
				"commits/00/00000000000000000000000000000000000001/applications/app2/.gitkeep",
			},
		},
	}

	for _, tc := range tcs {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			ctx := testutil.MakeTestContext()
			t.Parallel()
			repo := setupRepositoryTest(t)
			_, updatedState, _, err := repo.ApplyTransformersInternal(ctx, tc.Transformers...)
			if err != nil {
				t.Fatalf("encountered error but no error is expected here: %v", err)
			}
			fs := updatedState.Filesystem

			err = verifyCommitPathsExist(fs, tc.ExistentCommitPaths)
			if err != nil {
				t.Fatalf("some paths failed to create: %v", err)
			}

			err = verifyCommitPathsDontExist(fs, tc.NonExistentCommitPaths)
			if err != nil {
				t.Fatalf("some paths failed to delete: %v", err)
			}

			err = verifyConsistency(fs)
			if err != nil {
				t.Fatalf("inconsistent manifet repo: %v", err)
			}
		})
	}
}

func TestUndeployApplicationCommitPath(t *testing.T) {
	type TestCase struct {
		Name                   string
		Transformers           []Transformer
		ExistentCommitPaths    []string
		NonExistentCommitPaths []string
	}

	intToSHA1 := func(n int) string {
		ret := strconv.Itoa(n)
		ret = strings.Repeat("0", 40-len(ret)) + ret
		return ret
	}

	manyCreateApplication := func(app string, n int) []Transformer {
		ret := make([]Transformer, 0)

		for i := 1; i <= n; i++ {
			ret = append(ret, &CreateApplicationVersion{
				Application:    app,
				SourceCommitId: intToSHA1(i),
				Manifests: map[string]string{
					envAcceptance: "acceptance",
				},
				WriteCommitData: true,
			})
		}
		return ret
	}

	tcs := []TestCase{
		{
			Name: "Create one application with SHA1 commit ID and then undeploy it",
			Transformers: []Transformer{
				&CreateApplicationVersion{
					Application:     "app",
					SourceCommitId:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					WriteCommitData: true,
				},
				&CreateUndeployApplicationVersion{
					Application: "app",
				},
				&UndeployApplication{
					Application: "app",
				},
			},
			NonExistentCommitPaths: []string{
				"commits/aa/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/applications/app/.gitkeep",
			},
		},
		{
			Name: "Create two applications and then undeploy one of them",
			Transformers: []Transformer{
				&CreateApplicationVersion{
					Application:     "app1",
					SourceCommitId:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					WriteCommitData: true,
				},
				&CreateApplicationVersion{
					Application:     "app2",
					SourceCommitId:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					WriteCommitData: true,
				},
				&CreateUndeployApplicationVersion{
					Application: "app1",
				},
				&UndeployApplication{
					Application: "app1",
				},
			},
			ExistentCommitPaths: []string{
				"commits/aa/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/applications/app2/.gitkeep",
			},
			NonExistentCommitPaths: []string{
				"commits/aa/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/applications/app1/.gitkeep",
			},
		},
		{
			Name: "Create two applications many times and then undeploy one of them",
			Transformers: concatenate(
				manyCreateApplication("app1", 20),
				manyCreateApplication("app2", 20),
				[]Transformer{
					&CreateUndeployApplicationVersion{
						Application: "app2",
					},
					&UndeployApplication{
						Application: "app2",
					},
				},
			),
			ExistentCommitPaths: []string{
				"commits/00/00000000000000000000000000000000000001/applications/app1/.gitkeep",
				"commits/00/00000000000000000000000000000000000020/applications/app1/.gitkeep",
			},
			NonExistentCommitPaths: []string{
				"commits/00/00000000000000000000000000000000000001/applications/app2/.gitkeep",
				"commits/00/00000000000000000000000000000000000020/applications/app2/.gitkeep",
			},
		},
	}

	for _, tc := range tcs {
		t.Run(tc.Name, func(t *testing.T) {
			tc := tc
			ctx := testutil.MakeTestContext()
			t.Parallel()
			repo := setupRepositoryTest(t)
			_, updatedState, _, err := repo.ApplyTransformersInternal(ctx, tc.Transformers...)
			if err != nil {
				t.Fatalf("encountered error but no error is expected here: %v", err)
			}
			fs := updatedState.Filesystem

			err = verifyCommitPathsExist(fs, tc.ExistentCommitPaths)
			if err != nil {
				t.Fatalf("some paths failed to create: %v", err)
			}

			err = verifyCommitPathsDontExist(fs, tc.NonExistentCommitPaths)
			if err != nil {
				t.Fatalf("some paths failed to delete: %v", err)
			}

			err = verifyConsistency(fs)
			if err != nil {
				t.Fatalf("inconsistent manifet repo: %v", err)
			}
		})
	}
}

func TestDeployApplicationVersion(t *testing.T) {
	tcs := []struct {
		Name                        string
		Transformers                []Transformer
		expectedError               string
		expectedPath                string
		expectedFileData            []byte
		expectedDeployedByPath      string
		expectedDeployedByData      []byte
		expectedDeployedByEmailPath string
		expectedDeployedByEmailData []byte
		expectedDeployedAtPath      string
		expectedDeployedAtData      []byte
	}{
		{
			Name: "successfully deploy a full manifest",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "acceptance",
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: envAcceptance, Latest: false}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envAcceptance: "acceptance", // not empty
					},
					WriteCommitData: true,
				},
				&DeployApplicationVersion{
					Environment:   envAcceptance,
					Application:   "app1",
					Version:       1,
					LockBehaviour: api.LockBehavior_FAIL,
				},
			},
			expectedError:               "",
			expectedPath:                "environments/acceptance/applications/app1/manifests/manifests.yaml",
			expectedFileData:            []byte("acceptance"),
			expectedDeployedByPath:      "environments/acceptance/applications/app1/deployed_by",
			expectedDeployedByData:      []byte("test tester"),
			expectedDeployedAtPath:      "environments/acceptance/applications/app1/deployed_at_utc",
			expectedDeployedAtData:      []byte(timeNowOld.UTC().String()),
			expectedDeployedByEmailPath: "environments/acceptance/applications/app1/deployed_by_email",
			expectedDeployedByEmailData: []byte("testmail@example.com"),
		},
		{
			Name: "successfully deploy an empty manifest",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "acceptance",
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: envAcceptance, Latest: false}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envAcceptance: "", // empty!
					},
					WriteCommitData: true,
				},
				&DeployApplicationVersion{
					Environment:   envAcceptance,
					Application:   "app1",
					Version:       1,
					LockBehaviour: api.LockBehavior_FAIL,
				},
			},
			expectedError:               "",
			expectedPath:                "environments/acceptance/applications/app1/manifests/manifests.yaml",
			expectedFileData:            []byte(" "),
			expectedDeployedByPath:      "environments/acceptance/applications/app1/deployed_by",
			expectedDeployedByData:      []byte("test tester"),
			expectedDeployedAtPath:      "environments/acceptance/applications/app1/deployed_at_utc",
			expectedDeployedAtData:      []byte(timeNowOld.UTC().String()),
			expectedDeployedByEmailPath: "environments/acceptance/applications/app1/deployed_by_email",
			expectedDeployedByEmailData: []byte("testmail@example.com"),
		},
	}
	for _, tc := range tcs {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			ctxWithTime := WithTimeNow(testutil.MakeTestContext(), timeNowOld)
			t.Parallel()
			repo := setupRepositoryTest(t)
			_, updatedState, _, err := repo.ApplyTransformersInternal(ctxWithTime, tc.Transformers...)
			if err != nil {
				t.Fatalf("Expected no error when applying: %v", err)
			}

			fullPath := updatedState.Filesystem.Join(updatedState.Filesystem.Root(), tc.expectedPath)
			fileData, err := util.ReadFile(updatedState.Filesystem, fullPath)

			if err != nil {
				t.Fatalf("Expected no error: %v path=%s", err, fullPath)
			}
			if !cmp.Equal(fileData, tc.expectedFileData) {
				t.Fatalf("Expected '%v', got '%v'", string(tc.expectedFileData), string(fileData))
			}

			fullDeployedByPath := updatedState.Filesystem.Join(updatedState.Filesystem.Root(), tc.expectedDeployedByPath)
			deployedByData, err := util.ReadFile(updatedState.Filesystem, fullDeployedByPath)

			if err != nil {
				t.Fatalf("Expected no error: %v path=%s", err, fullDeployedByPath)
			}
			if !cmp.Equal(deployedByData, tc.expectedDeployedByData) {
				t.Fatalf("Expected '%v', got '%v'", string(tc.expectedDeployedByData), string(deployedByData))
			}

			fullDeployedByEmailPath := updatedState.Filesystem.Join(updatedState.Filesystem.Root(), tc.expectedDeployedByEmailPath)
			deployedByEmailData, err := util.ReadFile(updatedState.Filesystem, fullDeployedByEmailPath)

			if err != nil {
				t.Fatalf("Expected no error: %v path=%s", err, fullDeployedByEmailPath)
			}
			if !cmp.Equal(deployedByEmailData, tc.expectedDeployedByEmailData) {
				t.Fatalf("Expected '%v', got '%v'", string(tc.expectedDeployedByEmailData), string(deployedByEmailData))
			}

			fullDeployedAtPath := updatedState.Filesystem.Join(updatedState.Filesystem.Root(), tc.expectedDeployedAtPath)
			DeployedAtData, err := util.ReadFile(updatedState.Filesystem, fullDeployedAtPath)

			if err != nil {
				t.Fatalf("Expected no error: %v path=%s", err, fullDeployedAtPath)
			}
			if !cmp.Equal(DeployedAtData, tc.expectedDeployedAtData) {
				t.Fatalf("Expected '%v', got '%v'", string(tc.expectedDeployedAtData), string(DeployedAtData))
			}
		})
	}
}

func TestCreateApplicationVersionWithVersion(t *testing.T) {
	tcs := []struct {
		Name             string
		Transformers     []Transformer
		expectedPath     string
		expectedFileData []byte
	}{
		{
			Name: "successfully create app version with right order - should work",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "acceptance",
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: envAcceptance, Latest: true}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envAcceptance: "first version (100) manifest",
					},
					Version:         100,
					WriteCommitData: true,
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envAcceptance: "second version (101) manifest",
					},
					Version:         101,
					WriteCommitData: true,
				},
			},
			expectedPath:     "applications/app1/releases/101/environments/acceptance/manifests.yaml",
			expectedFileData: []byte("second version (101) manifest"),
		},
		{
			Name: "successfully create 2 app versions in wrong order - should work",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "acceptance",
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: envAcceptance, Latest: true}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envAcceptance: "first version (100) manifest",
					},
					Version:         100,
					WriteCommitData: true,
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envAcceptance: "second version (99) manifest",
					},
					Version:         99,
					WriteCommitData: true,
				},
			},
			expectedPath:     "applications/app1/releases/99/environments/acceptance/manifests.yaml",
			expectedFileData: []byte("second version (99) manifest"),
		},
		{
			Name: "successfully create app version with displayVersion",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "acceptance",
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: envAcceptance, Latest: true}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envAcceptance: "manifest",
					},
					Version:         100,
					DisplayVersion:  "1.3.1",
					WriteCommitData: true,
				},
			},
			expectedPath:     "applications/app1/releases/100/display_version",
			expectedFileData: []byte("1.3.1"),
		},
	}
	for _, tc := range tcs {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			repo := setupRepositoryTest(t)
			_, updatedState, _, _ := repo.ApplyTransformersInternal(testutil.MakeTestContext(), tc.Transformers...)

			fileData, err := util.ReadFile(updatedState.Filesystem, updatedState.Filesystem.Join(updatedState.Filesystem.Root(), tc.expectedPath))

			if err != nil {
				t.Fatalf("Expected no error: %v", err)
			}
			if !cmp.Equal(fileData, tc.expectedFileData) {
				t.Fatalf("Expected %v, got %v", string(tc.expectedFileData), string(fileData))
			}
		})
	}
}

// Tests various error cases in the prepare-Undeploy endpoint, specifically the error messages returned.
func TestUndeployErrors(t *testing.T) {
	tcs := []struct {
		Name              string
		Transformers      []Transformer
		expectedError     string
		expectedCommitMsg string
		shouldSucceed     bool
	}{
		{
			Name: "Access non-existent application",
			Transformers: []Transformer{
				&CreateUndeployApplicationVersion{
					Application: "app1",
				},
			},
			expectedError:     "cannot undeploy non-existing application 'app1'",
			expectedCommitMsg: "",
			shouldSucceed:     false,
		},
		{
			Name: "Success",
			Transformers: []Transformer{
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envProduction: "productionmanifest",
					},
					WriteCommitData: true,
				},
				&CreateUndeployApplicationVersion{
					Application: "app1",
				},
			},
			expectedError:     "",
			expectedCommitMsg: "created undeploy-version 2 of 'app1'\n",
			shouldSucceed:     true,
		},
		{
			Name: "Deploy after Undeploy should work",
			Transformers: []Transformer{
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envProduction: "productionmanifest",
					},
					WriteCommitData: true,
				},
				&CreateUndeployApplicationVersion{
					Application: "app1",
				},
				&CreateApplicationVersion{
					Application:     "app1",
					Manifests:       nil,
					SourceCommitId:  "",
					SourceAuthor:    "",
					SourceMessage:   "",
					WriteCommitData: true,
				},
			},
			expectedError:     "",
			expectedCommitMsg: "created version 3 of \"app1\"\n",
			shouldSucceed:     true,
		},
		{
			Name: "Undeploy twice should succeed",
			Transformers: []Transformer{
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envProduction: "productionmanifest",
					},
					WriteCommitData: true,
				},
				&CreateUndeployApplicationVersion{
					Application: "app1",
				},
				&CreateUndeployApplicationVersion{
					Application: "app1",
				},
			},
			shouldSucceed:     true,
			expectedError:     "",
			expectedCommitMsg: "created undeploy-version 3 of 'app1'\n",
		},
	}
	for _, tc := range tcs {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			repo := setupRepositoryTest(t)
			commitMsg, _, _, err := repo.ApplyTransformersInternal(testutil.MakeTestContext(), tc.Transformers...)
			// note that we only check the LAST error here:
			if tc.shouldSucceed {
				if err != nil {
					t.Fatalf("Expected no error: %v", err)
				}
				actualMsg := commitMsg[len(commitMsg)-1]
				if actualMsg != tc.expectedCommitMsg {
					t.Fatalf("expected a different message.\nExpected: %q\nGot %q", tc.expectedCommitMsg, actualMsg)
				}
			} else {
				if err == nil {
					t.Fatalf("Expected an error but got none")
				} else {
					actualMsg := err.Error()
					if actualMsg != tc.expectedError {
						t.Fatalf("expected a different error.\nExpected: %q\nGot %q", tc.expectedError, actualMsg)
					}
				}
			}
		})
	}
}

// Tests various error cases in the release train, specifically the error messages returned.
func TestReleaseTrainErrors(t *testing.T) {
	tcs := []struct {
		Name              string
		Transformers      []Transformer
		expectedError     string
		expectedCommitMsg string
		shouldSucceed     bool
	}{
		{
			Name: "Access non-existent environment",
			Transformers: []Transformer{
				&ReleaseTrain{
					Target: "doesnotexistenvironment",
				},
			},
			expectedError:     "rpc error: code = InvalidArgument desc = error: could not find environment group or environment configs for 'doesnotexistenvironment'",
			expectedCommitMsg: "",
			shouldSucceed:     false,
		},
		{
			Name: "Environment is locked - but train continues in other env",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: envAcceptance + "-de",
					Config: config.EnvironmentConfig{
						Upstream: &config.EnvironmentConfigUpstream{
							Latest: true,
						},
						EnvironmentGroup: ptr.FromString(envAcceptance),
					},
				},
				&CreateEnvironment{
					Environment: envAcceptance + "-ca",
					Config: config.EnvironmentConfig{
						Upstream: &config.EnvironmentConfigUpstream{
							Latest: true,
						},
						EnvironmentGroup: ptr.FromString(envAcceptance),
					},
				},
				&CreateEnvironmentLock{
					Environment: envAcceptance + "-ca",
					Message:     "don't",
					LockId:      "care",
				},
				&CreateEnvironmentLock{
					Environment: envAcceptance + "-de",
					Message:     "do not",
					LockId:      "care either",
				},
				&ReleaseTrain{
					Target: envAcceptance,
				},
			},
			shouldSucceed: true,
			expectedError: "",
			expectedCommitMsg: `Release Train to environment/environment group 'acceptance':

Release Train to 'acceptance-ca' environment:

Skipped services:
Target Environment 'acceptance-ca' is locked - skipping.


Release Train to 'acceptance-de' environment:

Skipped services:
Target Environment 'acceptance-de' is locked - skipping.


`,
		},
		{
			Name: "Environment has no upstream - but train continues in other env",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: envAcceptance + "-ca",
					Config: config.EnvironmentConfig{
						Upstream:         nil,
						EnvironmentGroup: ptr.FromString(envAcceptance),
					},
				},
				&CreateEnvironment{
					Environment: envAcceptance + "-de",
					Config: config.EnvironmentConfig{
						Upstream:         nil,
						EnvironmentGroup: ptr.FromString(envAcceptance),
					},
				},
				&ReleaseTrain{
					Target: envAcceptance,
				},
			},
			shouldSucceed: true,
			expectedError: "",
			expectedCommitMsg: `Release Train to environment/environment group 'acceptance':

Release Train to 'acceptance-ca' environment:

Skipped services:
Environment '"acceptance-ca"' does not have upstream configured - skipping.

Release Train to 'acceptance-de' environment:

Skipped services:
Environment '"acceptance-de"' does not have upstream configured - skipping.

`,
		},
		{
			Name: "Environment has no upstream.latest or env - but train continues in other env",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: envAcceptance + "-ca",
					Config: config.EnvironmentConfig{
						Upstream: &config.EnvironmentConfigUpstream{
							Environment: "",
							Latest:      false,
						},
						EnvironmentGroup: ptr.FromString(envAcceptance),
					},
				},
				&CreateEnvironment{
					Environment: envAcceptance + "-de",
					Config: config.EnvironmentConfig{
						Upstream: &config.EnvironmentConfigUpstream{
							Environment: "",
							Latest:      false,
						},
						EnvironmentGroup: ptr.FromString(envAcceptance),
					},
				},
				&ReleaseTrain{
					Target: envAcceptance,
				},
			},
			shouldSucceed: true,
			expectedError: "",
			expectedCommitMsg: `Release Train to environment/environment group 'acceptance':

Release Train to 'acceptance-ca' environment:

Skipped services:
Environment "acceptance-ca" does not have upstream.latest or upstream.environment configured - skipping.

Release Train to 'acceptance-de' environment:

Skipped services:
Environment "acceptance-de" does not have upstream.latest or upstream.environment configured - skipping.

`,
		},
		{
			Name: "Environment has both upstream.latest and env - but train continues in other env",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: envAcceptance + "-ca",
					Config: config.EnvironmentConfig{
						Upstream: &config.EnvironmentConfigUpstream{
							Environment: "dev",
							Latest:      true,
						},
						EnvironmentGroup: ptr.FromString(envAcceptance),
					},
				},
				&CreateEnvironment{
					Environment: envAcceptance + "-de",
					Config: config.EnvironmentConfig{
						Upstream: &config.EnvironmentConfigUpstream{
							Environment: "dev",
							Latest:      true,
						},
						EnvironmentGroup: ptr.FromString(envAcceptance),
					},
				},
				&ReleaseTrain{
					Target: envAcceptance,
				},
			},
			shouldSucceed: true,
			expectedError: "",
			expectedCommitMsg: `Release Train to environment/environment group 'acceptance':

Release Train to 'acceptance-ca' environment:

Skipped services:
Environment "acceptance-ca" has both upstream.latest and upstream.environment configured - skipping.

Release Train to 'acceptance-de' environment:

Skipped services:
Environment "acceptance-de" has both upstream.latest and upstream.environment configured - skipping.

`,
		},
	}
	for _, tc := range tcs {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			repo := setupRepositoryTest(t)
			commitMsg, _, _, err := repo.ApplyTransformersInternal(testutil.MakeTestContext(), tc.Transformers...)
			// note that we only check the LAST error here:
			if tc.shouldSucceed {
				if err != nil {
					t.Fatalf("Expected no error: %v", err)
				}
				actualMsg := commitMsg[len(commitMsg)-1]
				if diff := cmp.Diff(actualMsg, tc.expectedCommitMsg); diff != "" {
					t.Errorf("got \n%s\n, want \n%s\n, diff (-want +got)\n%s\n", actualMsg, tc.expectedCommitMsg, diff)
				}
			} else {
				if err == nil {
					t.Fatalf("Expected an error but got none")
				} else {
					actualMsg := err.Error()
					if actualMsg != tc.expectedError {
						t.Fatalf("expected a different error.\nExpected: %q\nGot %q", tc.expectedError, actualMsg)
					}
				}
			}
		})
	}
}

func TestTransformerChanges(t *testing.T) {
	tcs := []struct {
		Name              string
		Transformers      []Transformer
		expectedCommitMsg string
		expectedChanges   *TransformerResult
	}{
		{
			Name: "Deploy 1 app, another app locked by app lock",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: envProduction,
					Config:      testutil.MakeEnvConfigUpstream(envAcceptance, nil),
				},
				&CreateEnvironment{
					Environment: envAcceptance,
					Config:      testutil.MakeEnvConfigLatest(nil),
				},
				&CreateEnvironmentApplicationLock{
					Environment: envProduction,
					Application: "foo",
					LockId:      "foo-id",
					Message:     "foo",
				},
				&CreateApplicationVersion{
					Application: "foo",
					Manifests: map[string]string{
						envProduction: envProduction,
						envAcceptance: envAcceptance,
					},
					WriteCommitData: true,
				},
				&CreateApplicationVersion{
					Application: "bar",
					Manifests: map[string]string{
						envProduction: envProduction,
						envAcceptance: envAcceptance,
					},
					WriteCommitData: true,
				},
				&ReleaseTrain{
					Target: envProduction,
				},
			},
			expectedChanges: &TransformerResult{
				ChangedApps: []AppEnv{
					// foo is locked, so it should not appear here
					{
						App: "bar",
						Env: envProduction,
					},
				},
			},
		},
		{
			Name: "env lock",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: envProduction,
					Config:      testutil.MakeEnvConfigUpstream(envAcceptance, nil),
				},
				&CreateEnvironment{
					Environment: envAcceptance,
					Config:      testutil.MakeEnvConfigLatest(nil),
				},
				&CreateEnvironmentLock{
					Environment: envProduction,
					LockId:      "foo-id",
					Message:     "foo",
				},
				&CreateApplicationVersion{
					Application: "foo",
					Manifests: map[string]string{
						envProduction: envProduction,
						envAcceptance: envAcceptance,
					},
					WriteCommitData: true,
				},
				&CreateApplicationVersion{
					Application: "bar",
					Manifests: map[string]string{
						envProduction: envProduction,
						envAcceptance: envAcceptance,
					},
					WriteCommitData: true,
				},
				&ReleaseTrain{
					Target: envProduction,
				},
			},
			expectedChanges: &TransformerResult{
				ChangedApps: nil,
			},
		},
		{
			Name: "create env lock",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: envProduction,
					Config:      testutil.MakeEnvConfigUpstream(envAcceptance, nil),
				},
				&CreateEnvironmentLock{
					Environment: envProduction,
					LockId:      "foo-id",
					Message:     "foo",
				},
			},
			expectedChanges: &TransformerResult{
				ChangedApps: nil,
			},
		},
		{
			Name: "create env",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: envProduction,
					Config:      testutil.MakeEnvConfigUpstream(envAcceptance, nil),
				},
			},
			expectedChanges: &TransformerResult{
				ChangedApps: nil,
			},
		},
		{
			Name: "delete env from app",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: envAcceptance,
					Config:      testutil.MakeEnvConfigLatest(nil),
				},
				&CreateEnvironment{
					Environment: envProduction,
					Config:      testutil.MakeEnvConfigUpstream(envAcceptance, nil),
				},
				&CreateApplicationVersion{
					Application: "foo",
					Manifests: map[string]string{
						envProduction: envProduction,
						envAcceptance: envAcceptance,
					},
					WriteCommitData: true,
				},
				&DeleteEnvFromApp{
					Application: "foo",
					Environment: envAcceptance,
				},
			},
			expectedChanges: &TransformerResult{
				ChangedApps: []AppEnv{
					{
						App: "foo",
						Env: envAcceptance,
					},
				},
				DeletedRootApps: []RootApp{
					{
						Env: envAcceptance,
					},
				},
			},
		},
		{
			Name: "deploy",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: envAcceptance,
					Config:      testutil.MakeEnvConfigLatest(nil),
				},
				&CreateEnvironment{
					Environment: envProduction,
					Config:      testutil.MakeEnvConfigUpstream(envAcceptance, nil),
				},
				&CreateApplicationVersion{
					Application: "foo",
					Manifests: map[string]string{
						envProduction: envProduction,
						envAcceptance: envAcceptance,
					},
					WriteCommitData: true,
				},
				&DeployApplicationVersion{
					Authentication: Authentication{},
					Environment:    envProduction,
					Application:    "foo",
					Version:        1,
				},
			},
			expectedChanges: &TransformerResult{
				ChangedApps: []AppEnv{
					{
						App: "foo",
						Env: envProduction,
					},
				},
			},
		},
	}
	for _, tc := range tcs {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			repo := setupRepositoryTest(t)
			_, _, actualChanges, err := repo.ApplyTransformersInternal(testutil.MakeTestContext(), tc.Transformers...)
			// we only diff the changes from the last transformer here:
			lastChanges := actualChanges[len(actualChanges)-1]
			// note that we only check the LAST error here:
			if err != nil {
				t.Fatalf("Expected no error: %v", err)
			}

			if diff := cmp.Diff(lastChanges, tc.expectedChanges); diff != "" {
				t.Errorf("got %v, want %v, diff (-want +got) %s", lastChanges, tc.expectedChanges, diff)
			}
		})
	}
}

func TestRbacTransformerTest(t *testing.T) {
	envGroupProduction := "production"
	tcs := []struct {
		Name          string
		ctx           context.Context
		Transformers  []Transformer
		ExpectedError string
	}{
		{
			Name: "able to undeploy application with permissions policy",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment:    "staging",
					Config:         config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Latest: true}},
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&CreateEnvironment{
					Environment:    "production",
					Config:         config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: "staging"}},
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						"production": "production",
						"staging":    "staging",
					},
					Authentication:  Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
					WriteCommitData: true,
				},
				&CreateUndeployApplicationVersion{
					Application:    "app1",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&UndeployApplication{
					Application: "app1",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{
						"developer,DeployUndeploy,staging:*,app1,allow":    {Role: "developer"},
						"developer,DeployUndeploy,production:*,app1,allow": {Role: "developer"},
					}}},
				},
			},
		},
		{
			Name: "unable to undeploy application without permissions policy",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment:    "staging",
					Config:         config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Latest: true}},
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&CreateEnvironment{
					Environment:    "production",
					Config:         config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: "staging"}},
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						"production": "production",
						"staging":    "staging",
					},
					Authentication:  Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
					WriteCommitData: true,
				},
				&CreateUndeployApplicationVersion{
					Application:    "app1",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&UndeployApplication{
					Application: "app1",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{
						"developer,DeployUndeploy,production:*,app1,allow": {Role: "developer"},
					}}},
				},
			},
			ExpectedError: "PermissionDenied: The user 'test tester' with role 'developer' is not allowed to perform the action 'DeployUndeploy' on environment '*'",
		},
		{
			Name: "able to create environment with permissions policy",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "production-p1",
					Config:      config.EnvironmentConfig{EnvironmentGroup: nil},
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{
						"developer,CreateEnvironment,*:*,*,allow": {Role: "developer"}}}}},
			},
		},
		{
			Name: "able to create environment inside environment group with permissions policy",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "production-p2",
					Config:      config.EnvironmentConfig{EnvironmentGroup: &envGroupProduction},
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{
						"developer,CreateEnvironment,production:*,*,allow": {Role: "developer"}}}}},
			},
		},
		{
			Name: "unable to create environment without permissions policy",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment:    "production-p2",
					Config:         config.EnvironmentConfig{EnvironmentGroup: &envGroupProduction},
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{}}}},
			},
			ExpectedError: "PermissionDenied: The user 'test tester' with role 'developer' is not allowed to perform the action 'CreateEnvironment' on environment '*'",
		},
		{
			Name: "able to create undeploy with permissions policy",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment:    "staging",
					Config:         config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Latest: true}},
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&CreateEnvironment{
					Environment:    "production",
					Config:         config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: "staging"}},
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						"production": "production",
						"staging":    "staging",
					},
					Authentication:  Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
					WriteCommitData: true,
				},
				&CreateUndeployApplicationVersion{
					Application: "app1",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{
						"developer,CreateUndeploy,production:*,app1,allow": {Role: "developer"},
						"developer,CreateUndeploy,staging:*,app1,allow":    {Role: "developer"},
						"developer,DeployRelease,staging:*,app1,allow":     {Role: "developer"},
					}}},
				},
			},
		},
		{
			Name: "unable to create undeploy without permissions policy: Missing DeployRelease permission",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment:    "staging",
					Config:         config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Latest: true}},
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&CreateEnvironment{
					Environment:    "production",
					Config:         config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: "staging"}},
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						"production": "production",
						"staging":    "staging",
					},
					Authentication:  Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
					WriteCommitData: true,
				},
				&CreateUndeployApplicationVersion{
					Application: "app1",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{
						"developer,CreateUndeploy,production:*,app1,allow": {Role: "developer"},
						"developer,CreateUndeploy,staging:*,app1,allow":    {Role: "developer"},
					}}},
				},
			},
			ExpectedError: "PermissionDenied: The user 'test tester' with role 'developer' is not allowed to perform the action 'DeployRelease' on environment 'staging'",
		},
		{
			Name: "unable to create undeploy without permissions policy: Missing CreateUndeploy permission",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment:    "staging",
					Config:         config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Latest: true}},
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&CreateEnvironment{
					Environment:    "production",
					Config:         config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: "staging"}},
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						"production": "production",
						"staging":    "staging",
					},
					Authentication:  Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
					WriteCommitData: true,
				},
				&CreateUndeployApplicationVersion{
					Application: "app1",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{
						"developer,CreateUndeploy,production:*,app1,allow": {Role: "developer"},
					}}},
				},
			},
			ExpectedError: "PermissionDenied: The user 'test tester' with role 'developer' is not allowed to perform the action 'CreateUndeploy' on environment '*'",
		},
		{
			Name: "able to create release train with permissions policy",
			Transformers: ReleaseTrainTestSetup(&ReleaseTrain{
				Target: envProduction,
				Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{
					"developer,DeployReleaseTrain,production:production,*,allow": {Role: "developer"},
					"developer,DeployRelease,production:*,test,allow":            {Role: "developer"},
				}}},
			}),
		},
		{
			Name: "unable to create release train without permissions policy: Missing DeployRelease permission",
			Transformers: ReleaseTrainTestSetup(&ReleaseTrain{
				Target: envProduction,
				Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{
					"developer,DeployReleaseTrain,production:production,*,allow": {Role: "developer"},
				}}},
			}),
			ExpectedError: "rpc error: code = Internal desc = internal error",
		},
		{
			Name: "unable to create release train without permissions policy: Missing ReleaseTrain permission",
			Transformers: ReleaseTrainTestSetup(&ReleaseTrain{
				Target:         envProduction,
				Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{}}},
			}),
			ExpectedError: "PermissionDenied: The user 'test tester' with role 'developer' is not allowed to perform the action 'DeployReleaseTrain' on environment 'production'",
		},
		{
			Name: "able to create application version with permissions policy",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment:    "acceptance",
					Config:         config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Latest: true}},
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&CreateApplicationVersion{
					Application: "app1-testing",
					Manifests: map[string]string{
						envAcceptance: "acceptance", // not empty
					},
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{
						"developer,CreateRelease,acceptance:*,app1-testing,allow": {Role: "developer"},
						"developer,DeployRelease,acceptance:*,app1-testing,allow": {Role: "developer"},
					}}},
					WriteCommitData: true,
				},
			},
		},
		{
			Name: "able to create application version with permissions policy: Missing DeployRelease permission",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment:    "acceptance",
					Config:         config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Latest: true}},
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&CreateApplicationVersion{
					Application: "app1-testing",
					Manifests: map[string]string{
						envAcceptance: "acceptance", // not empty
					},
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{
						"developer,CreateRelease,acceptance:*,app1-testing,allow": {Role: "developer"},
					}}},
					WriteCommitData: true,
				},
			},
			ExpectedError: "PermissionDenied: The user 'test tester' with role 'developer' is not allowed to perform the action 'DeployRelease' on environment 'acceptance'",
		},
		{
			Name: "unable to create application version without permissions policy",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment:    "acceptance",
					Config:         config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Latest: true}},
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envAcceptance: "acceptance", // not empty
					},
					Authentication:  Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{}}},
					WriteCommitData: true,
				},
			},
			ExpectedError: "PermissionDenied: The user 'test tester' with role 'developer' is not allowed to perform the action 'CreateRelease' on environment '*'",
		},
		{
			Name: "able to deploy application with permissions policy",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment:    "acceptance",
					Config:         config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Latest: true}},
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envAcceptance: "acceptance", // not empty
					},
					Authentication:  Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
					WriteCommitData: true,
				},
				&DeployApplicationVersion{
					Environment:   envAcceptance,
					Application:   "app1",
					Version:       1,
					LockBehaviour: api.LockBehavior_FAIL,
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{
						"developer,DeployRelease,acceptance:acceptance,*,allow": {Role: "developer"}}}},
				},
			},
		},
		{
			Name: "unable to deploy application with permissions policy",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment:    "acceptance",
					Config:         config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Latest: true}},
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envAcceptance: "acceptance", // not empty
					},
					Authentication:  Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
					WriteCommitData: true,
				},
				&DeployApplicationVersion{
					Environment:    envAcceptance,
					Application:    "app1",
					Version:        1,
					LockBehaviour:  api.LockBehavior_FAIL,
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{}}},
				},
			},
			ExpectedError: "PermissionDenied: The user 'test tester' with role 'developer' is not allowed to perform the action 'DeployRelease' on environment 'acceptance'",
		},
		{
			Name: "able to create environment lock with permissions policy",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment:    "production",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&CreateEnvironmentLock{
					Environment: "production",
					Message:     "don't",
					LockId:      "manual",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{
						"developer,CreateLock,production:production,*,allow": {Role: "developer"}}}},
				},
			},
		},
		{
			Name: "able to create environment lock with permissions policy: different user",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment:    "production",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&CreateEnvironmentLock{
					Environment: "production",
					Message:     "don't",
					LockId:      "manual",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{
						"releaseManager,CreateLock,production:production,*,allow": {Role: "releaseManager"}}}},
				},
			},
			ctx: testutil.MakeTestContextDexEnabledUser("releaseManager"),
		},
		{
			Name: "unable to create environment lock without permissions policy",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment:    "production",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&CreateEnvironmentLock{
					Environment:    "production",
					Message:        "don't",
					LockId:         "manual",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{}}},
				},
			},
			ExpectedError: "PermissionDenied: The user 'test tester' with role 'developer' is not allowed to perform the action 'CreateLock' on environment 'production'",
		},
		{
			Name: "unable to delete environment lock without permissions policy",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment:    "production",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&CreateEnvironmentLock{
					Environment: "production",
					Message:     "don't",
					LockId:      "manual",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{
						"developer,CreateLock,production:production,*,allow": {Role: "developer"}}}},
				},
				&DeleteEnvironmentLock{
					Environment:    "production",
					LockId:         "manual",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{}}},
				},
			},
			ExpectedError: "PermissionDenied: The user 'test tester' with role 'developer' is not allowed to perform the action 'DeleteLock' on environment 'production'",
		},
		{
			Name: "able to delete environment lock with permissions policy",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment:    "production",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&CreateEnvironmentLock{
					Environment: "production",
					Message:     "don't",
					LockId:      "manual",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{
						"developer,CreateLock,production:production,*,allow": {Role: "developer"}}}},
				},
				&DeleteEnvironmentLock{
					Environment: "production",
					LockId:      "manual",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{
						"developer,DeployRelease,production:production,*,allow": {Role: "developer"},
						"developer,CreateLock,production:production,*,allow":    {Role: "developer"},
						"developer,DeleteLock,production:production,*,allow":    {Role: "developer"}}}},
				},
			},
		},
		{
			Name: "unable to create environment application lock without permissions policy",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment:    "production",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&CreateApplicationVersion{
					Application: "test",
					Manifests: map[string]string{
						"production": "productionmanifest",
					},
					Authentication:  Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
					WriteCommitData: true,
				},
				&CreateEnvironmentApplicationLock{
					Environment:    "production",
					Application:    "test",
					Message:        "don't",
					LockId:         "manual",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{}}},
				},
			},
			ExpectedError: "PermissionDenied: The user 'test tester' with role 'developer' is not allowed to perform the action 'CreateLock' on environment 'production'",
		},
		{
			Name: "able to create environment application lock with correct permissions policy",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment:    "production",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&CreateApplicationVersion{
					Application: "test",
					Manifests: map[string]string{
						"production": "productionmanifest",
					},
					Authentication:  Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
					WriteCommitData: true,
				},
				&CreateEnvironmentApplicationLock{
					Environment: "production",
					Application: "test",
					Message:     "don't",
					LockId:      "manual",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{
						"developer,CreateLock,production:production,*,allow": {Role: "Developer"},
					}}},
				},
			},
		},
		{
			Name: "unable to delete environment application lock without permissions policy",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment:    "production",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&CreateApplicationVersion{
					Application: "test",
					Manifests: map[string]string{
						"production": "productionmanifest",
					},
					Authentication:  Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
					WriteCommitData: true,
				},
				&CreateEnvironmentApplicationLock{
					Environment:    "production",
					Application:    "test",
					Message:        "don't",
					LockId:         "manual",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&DeleteEnvironmentApplicationLock{
					Environment:    "production",
					Application:    "test",
					LockId:         "manual",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{}}},
				},
			},
			ExpectedError: "PermissionDenied: The user 'test tester' with role 'developer' is not allowed to perform the action 'DeleteLock' on environment 'production'",
		},
		{
			Name: "able to delete environment application lock without permissions policy",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment:    "production",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&CreateApplicationVersion{
					Application: "test",
					Manifests: map[string]string{
						"production": "productionmanifest",
					},
					Authentication:  Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
					WriteCommitData: true,
				},
				&CreateEnvironmentApplicationLock{
					Environment: "production",
					Application: "test",
					Message:     "don't",
					LockId:      "manual",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{
						"developer,CreateLock,production:production,*,allow": {Role: "developer"},
					}}},
				},
				&DeleteEnvironmentApplicationLock{
					Environment: "production",
					Application: "test",
					LockId:      "manual",
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{
						"developer,DeleteLock,production:production,*,allow": {Role: "developer"},
					}}},
				},
			},
		},
		{
			Name: "unable to delete environment application without permission policy",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment:    envProduction,
					Config:         config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Latest: true}},
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envProduction: "productionmanifest",
					},
					Authentication:  Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
					WriteCommitData: true,
				},
				&DeployApplicationVersion{
					Environment:    envProduction,
					Application:    "app1",
					Version:        1,
					LockBehaviour:  api.LockBehavior_FAIL,
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&DeleteEnvFromApp{
					Application:    "app1",
					Environment:    envProduction,
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{}}},
				},
			},
			ExpectedError: "PermissionDenied: The user 'test tester' with role 'developer' is not allowed to perform the action 'DeleteEnvironmentApplication' on environment 'production'",
		},
		{
			Name: "able to delete environment application without permission policy",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment:    envProduction,
					Config:         config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Latest: true}},
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envProduction: "productionmanifest",
					},
					Authentication:  Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
					WriteCommitData: true,
				},
				&DeployApplicationVersion{
					Environment:    envProduction,
					Application:    "app1",
					Version:        1,
					LockBehaviour:  api.LockBehavior_FAIL,
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				&DeleteEnvFromApp{
					Application: "app1",
					Environment: envProduction,
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: true, Policy: map[string]*auth.Permission{
						"developer,DeleteEnvironmentApplication,production:production,*,allow": {Role: "developer"},
					}}},
				},
			},
		},
	}

	for _, tc := range tcs {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			dir := t.TempDir()
			remoteDir := path.Join(dir, "remote")
			localDir := path.Join(dir, "local")
			cmd := exec.Command("git", "init", "--bare", remoteDir)
			cmd.Start()
			cmd.Wait()
			ctx := testutil.MakeTestContextDexEnabled()
			if tc.ctx != nil {
				ctx = tc.ctx
			}
			repo, err := New(
				ctx,
				RepositoryConfig{
					URL:            remoteDir,
					Path:           localDir,
					CommitterEmail: "kuberpult@freiheit.com",
					CommitterName:  "kuberpult",
					BootstrapMode:  false,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			for _, tf := range tc.Transformers {
				err = repo.Apply(ctx, tf)
				if err != nil {
					break
				}
			}
			if err != nil {
				if !(strings.Contains(err.Error(), tc.ExpectedError)) {
					t.Errorf("want :\n\"%v\"\nbut got:\n\"%v\"", tc.ExpectedError, err.Error())
				}
				if tc.ExpectedError == "" {
					t.Errorf("expected success but got: %v", err.Error())
				}
			} else if tc.ExpectedError != "" {
				t.Errorf("expected error but got: none found")
			}
		})
	}
}

// Helper method to setup release train unit tests.
func ReleaseTrainTestSetup(releaseTrainTransformer Transformer) []Transformer {
	return append([]Transformer{
		&CreateEnvironment{
			Environment: envProduction,
			Config: config.EnvironmentConfig{
				Upstream: &config.EnvironmentConfigUpstream{
					Environment: envAcceptance, // train drives from acceptance to production
				},
			},
		},
		&CreateEnvironment{
			Environment: envAcceptance,
			Config: config.EnvironmentConfig{
				Upstream: &config.EnvironmentConfigUpstream{
					Latest: true,
				},
			},
		},
		&CreateApplicationVersion{
			Application: "test",
			Manifests: map[string]string{
				envProduction: "productionmanifest",
				envAcceptance: "acceptancenmanifest",
			},
			WriteCommitData: true,
		},
		&DeployApplicationVersion{
			Environment: envProduction,
			Application: "test",
			Version:     1,
		},
		&CreateApplicationVersion{
			Application: "test",
			Manifests: map[string]string{
				envProduction: "productionmanifest",
				envAcceptance: "acceptancenmanifest",
			},
			WriteCommitData: true,
		},
		&DeployApplicationVersion{
			Environment: envAcceptance,
			Application: "test",
			Version:     1,
		},
		&DeployApplicationVersion{
			Environment: envAcceptance,
			Application: "test",
			Version:     2,
		}}, releaseTrainTransformer)
}

func TestTransformer(t *testing.T) {
	c1 := config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Latest: true}}

	tcs := []struct {
		Name          string
		Transformers  []Transformer
		Test          func(t *testing.T, s *State)
		ErrorTest     func(t *testing.T, err error)
		BootstrapMode bool
	}{
		{
			Name:         "Create Versions and do not clean up because not enough versions",
			Transformers: makeTransformersForDelete(3),
			Test: func(t *testing.T, s *State) {
				{
					prodVersion, err := s.GetEnvironmentApplicationVersion(envProduction, "test")
					if err != nil {
						t.Fatal(err)
					}
					if prodVersion == nil || *prodVersion != 3 {
						t.Errorf("unexpected version: expected 3, actual %d", *prodVersion)
					}
					checkReleaseExists := func(v uint64) {
						_, err := s.GetApplicationRelease("test", v)
						if err != nil {
							t.Fatal(err)
						}
					}
					var v uint64
					for v = 1; v <= 3; v++ {
						checkReleaseExists(v)
					}
				}
			},
		},
		{
			Name:         "Create Versions and clean up because too many version",
			Transformers: makeTransformersForDelete(keptVersionsOnCleanup),
			Test: func(t *testing.T, s *State) {
				{
					prodVersion, err := s.GetEnvironmentApplicationVersion(envProduction, "test")
					if err != nil {
						t.Fatal(err)
					}
					if prodVersion == nil || *prodVersion != keptVersionsOnCleanup {
						t.Errorf("unexpected version: actual %d", *prodVersion)
					}
					checkReleaseExists := func(v uint64) {
						_, err := s.GetApplicationRelease("test", v)
						if err != nil {
							t.Fatal(err)
						}
					}
					var v uint64
					for v = 1; v <= keptVersionsOnCleanup; v++ {
						checkReleaseExists(v)
					}
				}
			},
		},
		{
			Name:         "Create Versions and clean up because too many version",
			Transformers: makeTransformersForDelete(keptVersionsOnCleanup + additionalVersions),
			Test: func(t *testing.T, s *State) {
				{
					prodVersion, err := s.GetEnvironmentApplicationVersion(envProduction, "test")
					if err != nil {
						t.Fatal(err)
					}
					if prodVersion == nil || *prodVersion != keptVersionsOnCleanup+additionalVersions {
						t.Errorf("unexpected version: actual %d", *prodVersion)
					}
					checkReleaseExists := func(v uint64) {
						_, err := s.GetApplicationRelease("test", v)
						if err != nil {
							t.Fatal(err)
						}
					}
					checkReleaseDoesNotExists := func(v uint64) {
						release, err := s.GetApplicationRelease("test", v)
						if err == nil {
							t.Fatalf("expected release to not exist. release: %d, actual: %d", v, release.Version)
						} else {
							expectedError := fmt.Sprintf("could not call stat 'applications/test/releases/%d': file does not exist", v)
							if err.Error() != expectedError {
								t.Errorf("unexpected error while checking release: \n%v\nExpected:\n%s", err.Error(), expectedError)
							}
						}
					}
					var v uint64
					for v = 1; v <= additionalVersions; v++ {
						checkReleaseDoesNotExists(v)
					}
					for v = additionalVersions + 1; v <= keptVersionsOnCleanup+additionalVersions; v++ {
						checkReleaseExists(v)
					}
				}
			},
		},
		{
			Name: "Release train",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: envProduction,
					Config: config.EnvironmentConfig{
						Upstream: &config.EnvironmentConfigUpstream{
							Environment: envAcceptance, // train drives from acceptance to production
						},
					},
				},
				&CreateEnvironment{
					Environment: envAcceptance,
					Config: config.EnvironmentConfig{
						Upstream: &config.EnvironmentConfigUpstream{
							Environment: envAcceptance,
							Latest:      true,
						},
					},
				},
				&CreateApplicationVersion{
					Application: "test",
					Manifests: map[string]string{
						envProduction: "productionmanifest",
						envAcceptance: "acceptancenmanifest",
					},
					WriteCommitData: true,
				},
				&DeployApplicationVersion{
					Environment: envProduction,
					Application: "test",
					Version:     1,
				},
				&CreateApplicationVersion{
					Application: "test",
					Manifests: map[string]string{
						envProduction: "productionmanifest",
						envAcceptance: "acceptancenmanifest",
					},
					WriteCommitData: true,
				},
				&DeployApplicationVersion{
					Environment: envAcceptance,
					Application: "test",
					Version:     1,
				},
				&DeployApplicationVersion{
					Environment: envAcceptance,
					Application: "test",
					Version:     2,
				},
				&ReleaseTrain{
					Target: envProduction,
				},
			},
			Test: func(t *testing.T, s *State) {
				{
					prodVersion, err := s.GetEnvironmentApplicationVersion(envProduction, "test")
					if err != nil {
						t.Fatal(err)
					}
					acceptanceVersion, err := s.GetEnvironmentApplicationVersion(envAcceptance, "test")
					if err != nil {
						t.Fatal(err)
					}
					if *acceptanceVersion != 2 {
						t.Errorf("unexpected version: expected 2, actual %d", acceptanceVersion)
					}
					if *prodVersion != 2 {
						t.Errorf("unexpected version: expected 2, actual %d", *prodVersion)
					}
				}
			},
		},
		{
			Name: "Release train from Latest",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: envAcceptance,
					Config: config.EnvironmentConfig{
						Upstream: &config.EnvironmentConfigUpstream{
							Latest: true,
						},
					},
				},
				&CreateApplicationVersion{
					Application: "test",
					Manifests: map[string]string{
						envAcceptance: "acceptancenmanifest",
					},
					WriteCommitData: true,
				},
				&CreateApplicationVersion{
					Application: "test",
					Manifests: map[string]string{
						envAcceptance: "acceptancenmanifest",
					},
					WriteCommitData: true,
				},
				&ReleaseTrain{
					Target: envAcceptance,
				},
			},
			Test: func(t *testing.T, s *State) {
				{
					acceptanceVersion, err := s.GetEnvironmentApplicationVersion(envAcceptance, "test")
					if err != nil {
						t.Fatal(err)
					}
					if *acceptanceVersion != 2 {
						t.Errorf("unexpected version: expected 2, actual %d", acceptanceVersion)
					}
				}
			},
		},
		{
			Name: "Release train for a Team",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: envProduction,
					Config: config.EnvironmentConfig{
						Upstream: &config.EnvironmentConfigUpstream{
							Environment: envAcceptance, // train drives from acceptance to production
						},
					},
				},
				&CreateEnvironment{
					Environment: envAcceptance,
					Config: config.EnvironmentConfig{
						Upstream: &config.EnvironmentConfigUpstream{
							Environment: envAcceptance,
							Latest:      true,
						},
					},
				},
				&CreateApplicationVersion{
					Application: "test",
					Manifests: map[string]string{
						envProduction: "productionmanifest",
						envAcceptance: "acceptancenmanifest",
					},
					Team:            "test",
					WriteCommitData: true,
				},
				&DeployApplicationVersion{
					Environment: envProduction,
					Application: "test",
					Version:     1,
				},
				&CreateApplicationVersion{
					Application: "test",
					Manifests: map[string]string{
						envProduction: "productionmanifest",
						envAcceptance: "acceptancenmanifest",
					},
					WriteCommitData: true,
				},
				&DeployApplicationVersion{
					Environment: envAcceptance,
					Application: "test",
					Version:     1,
				},
				&DeployApplicationVersion{
					Environment: envAcceptance,
					Application: "test",
					Version:     2,
				},
				&ReleaseTrain{
					Target: envProduction,
					Team:   "test",
				},
			},
			Test: func(t *testing.T, s *State) {
				{
					prodVersion, err := s.GetEnvironmentApplicationVersion(envProduction, "test")
					if err != nil {
						t.Fatal(err)
					}
					acceptanceVersion, err := s.GetEnvironmentApplicationVersion(envAcceptance, "test")
					if err != nil {
						t.Fatal(err)
					}
					if *acceptanceVersion != 2 {
						t.Errorf("unexpected version: expected 2, actual %d", acceptanceVersion)
					}
					if *prodVersion != 2 {
						t.Errorf("unexpected version: expected 2, actual %d", *prodVersion)
					}
				}
			},
		},
		{
			Name: "Lock environment",
			Transformers: []Transformer{
				&CreateEnvironment{Environment: "production"},
				&CreateEnvironmentLock{
					Environment: "production",
					Message:     "don't",
					LockId:      "manual",
				},
			},
			Test: func(t *testing.T, s *State) {
				locks, err := s.GetEnvironmentLocks("production")
				if err != nil {
					t.Fatal(err)
				}
				expected := map[string]Lock{
					"manual": {
						Message: "don't",
						CreatedBy: Actor{
							Name:  "test tester",
							Email: "testmail@example.com",
						},
						CreatedAt: timeNowOld,
					},
				}
				if !reflect.DeepEqual(locks, expected) {
					t.Fatalf("mismatched locks. expected:\n%#v\nactual:\n%#v", expected, locks)
				}
			},
		},
		{
			Name: "Lock application",
			Transformers: []Transformer{
				&CreateEnvironment{Environment: "production"},
				&CreateApplicationVersion{
					Application: "test",
					Manifests: map[string]string{
						"production": "productionmanifest",
					},
					WriteCommitData: true,
				},
				&CreateEnvironmentApplicationLock{
					Environment: "production",
					Application: "test",
					Message:     "don't",
					LockId:      "manual",
				},
			},
			Test: func(t *testing.T, s *State) {
				locks, err := s.GetEnvironmentApplicationLocks("production", "test")
				if err != nil {
					t.Fatal(err)
				}
				expected := map[string]Lock{
					"manual": {
						Message: "don't",
						CreatedBy: Actor{
							Name:  "test tester",
							Email: "testmail@example.com",
						},
						CreatedAt: timeNowOld,
					},
				}
				if !reflect.DeepEqual(locks, expected) {
					t.Fatalf("mismatched locks. expected:\n%#v\n, actual:\n%#v", expected, locks)
				}
			},
		},
		{
			Name: "Overwriting lock environment",
			Transformers: []Transformer{
				&CreateEnvironment{Environment: "production"},
				&CreateEnvironmentLock{
					Environment: "production",
					Message:     "don't",
					LockId:      "manual",
				},
				&CreateEnvironmentLock{
					Environment: "production",
					Message:     "just don't",
					LockId:      "manual",
				},
			},
			Test: func(t *testing.T, s *State) {
				locks, err := s.GetEnvironmentLocks("production")
				if err != nil {
					t.Fatal(err)
				}
				expected := map[string]Lock{
					"manual": {
						Message: "just don't",
						CreatedBy: Actor{
							Name:  "test tester",
							Email: "testmail@example.com",
						},
						CreatedAt: timeNowOld,
					},
				}
				if !reflect.DeepEqual(locks, expected) {
					t.Fatalf("mismatched locks. expected: %#v, actual: %#v", expected, locks)
				}
			},
		},
		{
			Name: "Unlocking a locked environment",
			Transformers: []Transformer{
				&CreateEnvironment{Environment: "production"},
				&CreateEnvironmentLock{
					Environment: "production",
					Message:     "don't",
					LockId:      "manual",
				},
				&DeleteEnvironmentLock{
					Environment: "production",
					LockId:      "manual",
				},
			},
			Test: func(t *testing.T, s *State) {
				locks, err := s.GetEnvironmentLocks("production")
				if err != nil {
					t.Fatal(err)
				}
				expected := map[string]Lock{}
				if !reflect.DeepEqual(locks, expected) {
					t.Fatalf("mismatched locks. expected: %#v, actual: %#v", expected, locks)
				}
			},
		},
		{
			Name: "Unlocking an already unlocked environment",
			Transformers: []Transformer{
				&CreateEnvironment{Environment: "production"},
				&DeleteEnvironmentLock{
					Environment: "production",
					LockId:      "manual",
				},
			},
			ErrorTest: func(t *testing.T, actualError error) {
				expectedError := "directory environments/production/locks/manual for env lock does not exist"
				if !strings.Contains(actualError.Error(), expectedError) {
					t.Fatalf("mismatched error. expected: %#v, actual: %#v", expectedError, actualError)
				}
			},
		},
		{
			Name: "Deploy version",
			Transformers: []Transformer{
				&CreateEnvironment{Environment: "production"},
				&CreateApplicationVersion{
					Application: "test",
					Manifests: map[string]string{
						"production": "productionmanifest",
					},
					WriteCommitData: true,
				},
				&DeployApplicationVersion{
					Environment: "production",
					Application: "test",
					Version:     1,
				},
			},
			Test: func(t *testing.T, s *State) {
				// check that the state reads the correct versions
				{
					i, err := s.GetEnvironmentApplicationVersion("production", "test")
					if err != nil {
						t.Fatal(err)
					}
					if *i != 1 {
						t.Errorf("unexpected version: expected 1, actual %d", i)
					}
				}
				// check that the manifest is in place for argocd
				{
					m, err := s.Filesystem.Open("environments/production/applications/test/manifests/manifests.yaml")
					if err != nil {
						t.Fatal(err)
					}
					content, err := io.ReadAll(m)
					expected := "productionmanifest"
					actual := string(content)
					if actual != expected {
						t.Errorf("unexpected manifest: expected %q, actual: %q", expected, actual)
					}
				}
				// Check that reading is possible
				{
					rel, err := s.GetApplicationRelease("test", 1)
					if err != nil {
						t.Fatal(err)
					}
					if rel.Version != 1 {
						t.Errorf("unexpected version: expected 1, actual: %d", rel.Version)
					}
					if rel.SourceAuthor != "" {
						t.Errorf("unexpected source author: expected \"\", actual: %q", rel.SourceAuthor)
					}
					if rel.SourceCommitId != "" {
						t.Errorf("unexpected source commit id: expected \"\", actual: %q", rel.SourceCommitId)
					}
					if rel.SourceMessage != "" {
						t.Errorf("unexpected source author: expected \"\", actual: %q", rel.SourceMessage)
					}
				}
			},
		},
		{
			Name: "Create version with source information",
			Transformers: []Transformer{
				&CreateEnvironment{Environment: "production"},
				&CreateApplicationVersion{
					Application:    "test",
					SourceAuthor:   "test <test@example.com>",
					SourceCommitId: "deadbeef",
					SourceMessage:  "changed something",
					Manifests: map[string]string{
						"production": "productionmanifest",
					},
					WriteCommitData: true,
				},
			},
			Test: func(t *testing.T, s *State) {
				// Check that reading is possible
				{
					rel, err := s.GetApplicationRelease("test", 1)
					if err != nil {
						t.Fatal(err)
					}
					if rel.Version != 1 {
						t.Errorf("unexpected version: expected 1, actual: %d", rel.Version)
					}
					if rel.SourceAuthor != "test <test@example.com>" {
						t.Errorf("unexpected source author: expected \"test <test@example.com>\", actual: %q", rel.SourceAuthor)
					}
					if rel.SourceCommitId != "deadbeef" {
						t.Errorf("unexpected source commit id: expected \"deadbeef\", actual: %q", rel.SourceCommitId)
					}
					if rel.SourceMessage != "changed something" {
						t.Errorf("unexpected source author: expected \"changed something\", actual: %q", rel.SourceMessage)
					}
					if rel.CreatedAt != timeNowOld {
						t.Errorf("unexpected created at: expected: %q, actual: %q", timeNowOld, rel.SourceMessage)
					}
				}
			},
		}, {
			Name: "Create version with team name",
			Transformers: []Transformer{
				&CreateEnvironment{Environment: "production"},
				&CreateApplicationVersion{
					Application: "test",
					Manifests: map[string]string{
						"production": "productionmanifest",
					},
					Team:            "test-team",
					WriteCommitData: true,
				},
			},
			Test: func(t *testing.T, s *State) {
				// Check that team is written
				{
					team, err := s.GetApplicationTeamOwner("test")
					if err != nil {
						t.Fatal(err)
					}
					if team != "test-team" {
						t.Errorf("expected team name to be test-team, but got %q", team)
					}
				}
			},
		}, {
			Name: "Create version with version number",
			Transformers: []Transformer{
				&CreateEnvironment{Environment: "production"},
				&CreateApplicationVersion{
					Version:     42,
					Application: "test",
					Manifests: map[string]string{
						"production": "productionmanifest",
					},
					WriteCommitData: true,
				},
			},
			Test: func(t *testing.T, s *State) {
				// Check that reading is possible
				{
					rel, err := s.GetApplicationReleases("test")
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(rel, []uint64{42}) {
						t.Errorf("expected release list to be exaclty [42], but got %q", rel)
					}

				}
			},
		}, {
			Name: "Creating a version with same version number yields the correct error",
			Transformers: []Transformer{
				&CreateEnvironment{Environment: "production"},
				&CreateApplicationVersion{
					Version:     42,
					Application: "test",
					Manifests: map[string]string{
						"production": "productionmanifest",
					},
					WriteCommitData: true,
				},
				&CreateApplicationVersion{
					Version:     42,
					Application: "test",
					Manifests: map[string]string{
						"production": "productionmanifest",
					},
					WriteCommitData: true,
				},
			},
			ErrorTest: func(t *testing.T, err error) {
				expected := "already_exists_same:{}"
				if err.Error() != expected {
					t.Fatalf("expected: %s, got: %s", expected, err.Error())
				}
			},
		}, {
			Name: "Creating an older version doesn't auto deploy",
			Transformers: []Transformer{
				&CreateEnvironment{Environment: "production", Config: c1},
				&CreateApplicationVersion{
					Version:     42,
					Application: "test",
					Manifests: map[string]string{
						"production": "42",
					},
					WriteCommitData: true,
				},
				&CreateApplicationVersion{
					Version:     41,
					Application: "test",
					Manifests: map[string]string{
						"production": "41",
					},
					WriteCommitData: true,
				},
			},
			Test: func(t *testing.T, s *State) {
				i, err := s.GetEnvironmentApplicationVersion("production", "test")
				if err != nil {
					t.Fatal(err)
				}
				if *i != 42 {
					t.Errorf("unexpected version: expected 42, actual %d", i)
				}
			},
		}, {
			Name: "Creating a version that is much too old yields the correct error",
			Transformers: func() []Transformer {
				t := make([]Transformer, 0, keptVersionsOnCleanup+1)
				t = append(t, &CreateEnvironment{Environment: "production"})
				for i := keptVersionsOnCleanup + 1; i > 0; i-- {
					t = append(t, &CreateApplicationVersion{
						Version:     uint64(i),
						Application: "test",
						Manifests: map[string]string{
							"production": "42",
						},
						WriteCommitData: true,
					})
				}
				return t
			}(),
			ErrorTest: func(t *testing.T, err error) {
				expected := "too_old:{}"
				if err.Error() != expected {
					t.Fatalf("expected: %s, got: %s", expected, err.Error())
				}
			},
		}, {
			Name: "Auto Deploy version to second env",
			Transformers: []Transformer{
				&CreateEnvironment{Environment: "one", Config: c1},
				&CreateEnvironment{Environment: "two", Config: c1},
				&CreateApplicationVersion{
					Application: "test",
					Manifests: map[string]string{
						"one": "productionmanifest",
						"two": "productionmanifest",
					},
					WriteCommitData: true,
				},
				&DeployApplicationVersion{
					Environment: "one",
					Application: "test",
					Version:     1,
				},
			},
			Test: func(t *testing.T, s *State) {
				// check that the state reads the correct versions
				{
					i, err := s.GetEnvironmentApplicationVersion("one", "test")
					if err != nil {
						t.Fatal(err)
					}
					if *i != 1 {
						t.Errorf("unexpected version: expected 1, actual %d", i)
					}
				}
				for _, env := range []string{"one", "two"} {
					// check that the manifest is in place for BOTH envs

					m, err := s.Filesystem.Open(fmt.Sprintf("environments/%s/applications/test/manifests/manifests.yaml", env))
					if err != nil {
						t.Fatal(err)
					}
					content, err := io.ReadAll(m)
					expected := "productionmanifest"
					actual := string(content)
					if actual != expected {
						t.Errorf("unexpected manifest: expected %q, actual: %q", expected, actual)
					}
				}
			},
		},
		{
			Name: "Skip Auto Deploy if env is locked",
			Transformers: []Transformer{
				&CreateEnvironment{Environment: "one", Config: c1},
				&CreateEnvironment{Environment: "two", Config: c1},
				&CreateEnvironmentLock{
					Environment: "one",
					Message:     "don't!",
					LockId:      "manual123",
				},
				&CreateApplicationVersion{
					Application: "test",
					Manifests: map[string]string{
						"one": "productionmanifest",
						"two": "productionmanifest",
					},
					WriteCommitData: true,
				},
			},
			Test: func(t *testing.T, s *State) {
				// check that the state reads the correct versions
				{
					// version should only exist for "two"
					i, err := s.GetEnvironmentApplicationVersion("two", "test")
					if err != nil {
						t.Fatal(err)
					}
					if *i != 1 {
						t.Errorf("unexpected version: expected 1, actual %d", i)
					}
					i, err = s.GetEnvironmentApplicationVersion("one", "test")
					if i != nil || err != nil {
						t.Fatalf("expect file to not exist, because the env is locked.")
					}
				}
				// manifests should be written either way:
				for _, env := range []string{"one", "two"} {
					m, err := s.Filesystem.Open(fmt.Sprintf("applications/test/releases/1/environments/%s/manifests.yaml", env))
					if err != nil {
						t.Fatal(err)
					}
					content, err := io.ReadAll(m)
					expected := "productionmanifest"
					actual := string(content)
					if actual != expected {
						t.Errorf("unexpected manifest: expected %q, actual: %q", expected, actual)
					}
				}
			},
		},
		{
			Name: "Skip Auto Deploy version to second env if it's not latest",
			Transformers: []Transformer{
				&CreateEnvironment{Environment: "one", Config: c1},
				&CreateEnvironment{Environment: "two", Config: config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{
					Environment: "two",
					Latest:      false,
				}}},
				&CreateApplicationVersion{
					Application: "test",
					Manifests: map[string]string{
						"one": "productionmanifest",
						"two": "productionmanifest",
					},
					WriteCommitData: true,
				},
				&DeployApplicationVersion{
					Environment: "one",
					Application: "test",
					Version:     1,
				},
			},
			Test: func(t *testing.T, s *State) {
				// check that the state reads the correct versions
				{
					i, err := s.GetEnvironmentApplicationVersion("one", "test")
					if err != nil {
						t.Fatal(err)
					}
					if *i != 1 {
						t.Errorf("unexpected version: expected 1, actual %d", i)
					}
				}
				_, err := s.Filesystem.Open(fmt.Sprintf("environments/%s/applications/test/manifests/manifests.yaml", "two"))
				if err == nil {
					t.Fatal("expected not to find this file!")
				}
			},
		},
		{
			Name:         "Deploy version when environment is locked fails LockBehavior=Fail",
			Transformers: makeTransformersDeployTestEnvLock(api.LockBehavior_FAIL),
			ErrorTest: func(t *testing.T, err error) {
				var lockErr *LockedError
				if !errors.As(err, &lockErr) {
					t.Errorf("error must be a LockError, but got %#v", err)
				} else {
					expectedEnvLocks := map[string]Lock{
						"manual": {
							Message: "don't",
						},
					}
					if !reflect.DeepEqual(expectedEnvLocks["manual"].Message, lockErr.EnvironmentLocks["manual"].Message) {
						t.Errorf("unexpected environment locks: expected %q, actual: %q", expectedEnvLocks, lockErr.EnvironmentLocks)
					}
				}
			},
		},
		{
			Name:         "Deploy version ignoring locks when environment is locked LockBehavior=Ignore",
			Transformers: makeTransformersDeployTestEnvLock(api.LockBehavior_IGNORE),
			Test: func(t *testing.T, s *State) {
				// check that the state reads the correct versions
				i, err := s.GetEnvironmentApplicationVersion("production", "test")
				if err != nil {
					t.Fatal(err)
				}
				if *i != 1 {
					t.Errorf("unexpected version: expected 1, actual %d", i)
				}
			},
		},
		{
			Name:         "Deploy version ignoring locks when environment is locked LockBehavior=Queue",
			Transformers: makeTransformersDeployTestEnvLock(api.LockBehavior_RECORD),
			Test: func(t *testing.T, s *State) {
				// check that the state reads the correct versions
				i, err := s.GetEnvironmentApplicationVersion("production", "test")
				if err != nil {
					t.Fatal(err)
				}
				if i != nil {
					t.Errorf("unexpected version: expected nil, actual %d", i)
				}
			},
		},
		{
			Name:         "Deploy version when application in environment is locked and config=LockBehaviourIgnoreAllLocks",
			Transformers: makeTransformersDeployTestAppLock(api.LockBehavior_IGNORE),
			Test: func(t *testing.T, s *State) {
				// check that the state reads the correct versions
				i, err := s.GetEnvironmentApplicationVersion("production", "test")
				if err != nil {
					t.Fatal(err)
				}
				if *i != 1 {
					t.Errorf("unexpected version: expected 1, actual %d", i)
				}
			},
		},
		{
			Name:         "Deploy version when application in environment is locked and config=LockBehaviourQueue",
			Transformers: makeTransformersDeployTestAppLock(api.LockBehavior_RECORD),
			Test: func(t *testing.T, s *State) {
				// check that the state reads the correct versions
				i, err := s.GetEnvironmentApplicationVersion("production", "test")
				if err != nil && err.Error() != "file does not exist" {
					t.Fatalf("unexpected error: %v", err.Error())
				}
				if i != nil {
					t.Errorf("unexpected version: expected nil, actual %d", i)
				}

				actualQueued, err := s.GetQueuedVersion("production", "test")
				if err != nil {
					t.Fatal(err)
				}
				if *actualQueued != 1 {
					t.Errorf("unexpected version: expected 1, actual %d", i)
				}
			},
		},
		{
			Name:         "Deploy version ignoring locks when application in environment is locked and LockBehaviourFail",
			Transformers: makeTransformersDeployTestAppLock(api.LockBehavior_FAIL),
			ErrorTest: func(t *testing.T, err error) {
				var lockErr *LockedError
				if !errors.As(err, &lockErr) {
					t.Errorf("error must be a LockError, but got %#v", err)
				} else {
					expectedEnvLocks := map[string]Lock{
						"manual": {
							Message: "don't",
						},
					}
					if !reflect.DeepEqual(expectedEnvLocks["manual"].Message, lockErr.EnvironmentApplicationLocks["manual"].Message) {
						t.Errorf("unexpected environment locks: expected %q, actual: %q", expectedEnvLocks, lockErr.EnvironmentApplicationLocks)
					}
				}
			},
		},
		{
			Name:         "Deploy twice LockBehavior=Queue and LockBehavior=Queue",
			Transformers: makeTransformersTwoDeploymentsWriteToQueue(api.LockBehavior_RECORD, api.LockBehavior_RECORD),
			Test: func(t *testing.T, s *State) {
				// check that the state reads the correct versions
				i, err := s.GetEnvironmentApplicationVersion("production", "test")
				if err != nil {
					t.Fatal(err)
				}
				if i != nil {
					t.Errorf("unexpected version: expected nil, actual %d", i)
				}
				q, err := s.GetQueuedVersion("production", "test")
				if err != nil {
					t.Fatal(err)
				}
				if q == nil {
					t.Errorf("unexpected version: expected 2, actual nil")
				} else {
					if *q != 2 {
						t.Errorf("unexpected version: expected 2, actual %d", *q)
					}
				}
			},
		},
		{
			Name:         "Deploy twice LockBehavior=Queue and LockBehavior=Ignore",
			Transformers: makeTransformersTwoDeploymentsWriteToQueue(api.LockBehavior_RECORD, api.LockBehavior_IGNORE),
			Test: func(t *testing.T, s *State) {
				// check that the state reads the correct versions
				i, err := s.GetEnvironmentApplicationVersion("production", "test")
				if err != nil {
					t.Fatal(err)
				}
				if i == nil {
					t.Errorf("unexpected version: expected 2, actual nil")
				} else {
					if *i != 2 {
						t.Errorf("unexpected version: expected 2, actual %d", *i)
					}
				}
				q, err := s.GetQueuedVersion("production", "test")
				if err != nil {
					t.Fatal(err)
				}
				if q != nil {
					t.Errorf("unexpected version: expected nil, actual %d The queue should have been removed at this point!", *q)
				}
			},
		},
		{
			Name:         "Deploy twice LockBehavior=Ignore and LockBehavior=Queue",
			Transformers: makeTransformersTwoDeploymentsWriteToQueue(api.LockBehavior_IGNORE, api.LockBehavior_RECORD),
			Test: func(t *testing.T, s *State) {
				// check that the state reads the correct versions
				i, err := s.GetEnvironmentApplicationVersion("production", "test")
				if err != nil {
					t.Fatal(err)
				}
				if i == nil {
					t.Errorf("unexpected version: expected 1, actual nil")
				} else {
					if *i != 1 {
						t.Errorf("unexpected version: expected 1, actual %d", *i)
					}
				}
				q, err := s.GetQueuedVersion("production", "test")
				if err != nil {
					t.Fatal(err)
				}
				if q == nil {
					t.Errorf("unexpected version: expected 2, actual nil")
				} else {
					if *q != 2 {
						t.Errorf("unexpected version: expected 2, actual %d", *q)
					}
				}
			},
		},
		{
			Name:         "Lock env AND app and then Deploy and unlock one lock ",
			Transformers: makeTransformersDoubleLock(api.LockBehavior_RECORD, false),
			Test: func(t *testing.T, s *State) {
				// check that the state reads the correct versions
				i, err := s.GetEnvironmentApplicationVersion("production", "test")
				if err != nil {
					t.Fatal(err)
				}
				if i != nil {
					t.Errorf("unexpected version: expected nil, actual %d", *i)
				}
				q, err := s.GetQueuedVersion("production", "test")
				if err != nil {
					t.Fatal(err)
				}
				if q == nil {
					t.Errorf("unexpected version: expected 1, actual nil")
				} else {
					if *q != 1 {
						t.Errorf("unexpected version: expected 1, actual %d", *q)
					}
				}
			},
		},
		{
			Name:         "Lock env AND app and then Deploy and unlock both locks",
			Transformers: makeTransformersDoubleLock(api.LockBehavior_RECORD, true),
			Test: func(t *testing.T, s *State) {
				// check that the state reads the correct versions
				i, err := s.GetEnvironmentApplicationVersion("production", "test")
				if err != nil {
					t.Fatal(err)
				}
				if i != nil {
					t.Errorf("unexpected version %d: expected: nil", *i)
				}
				q, err := s.GetQueuedVersion("production", "test")
				if err != nil {
					t.Fatal(err)
				}
				if q == nil {
					t.Errorf("unexpected version: expected 1, actual nil")
				}
			},
		},
		{
			Name: "It creates an ArgoCd AppProject",
			Transformers: []Transformer{
				&CreateEnvironment{Environment: "staging", Config: config.EnvironmentConfig{
					ArgoCd: &config.EnvironmentConfigArgoCd{
						Destination: config.ArgoCdDestination{
							Namespace: ptr.FromString("staging"),
							Server:    "localhost:8080",
						},
					},
				}},
				&CreateEnvironment{Environment: "production", Config: config.EnvironmentConfig{
					ArgoCd: &config.EnvironmentConfigArgoCd{
						Destination: config.ArgoCdDestination{
							Name: "production",
						},
					},
				}},
				&CreateApplicationVersion{
					Application: "test",
					Manifests: map[string]string{
						"staging":    "stagingmanifest",
						"production": "stagingmanifest",
					},
					WriteCommitData: true,
				},
			},
			Test: func(t *testing.T, s *State) {
				// check that the state reads the correct versions
				{
					content, err := util.ReadFile(s.Filesystem, "argocd/v1alpha1/staging.yaml")
					if err != nil {
						t.Fatalf("unexpected error reading argocd manifest: %q", err)
					}
					expected := `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: staging
spec:
  description: staging
  destinations:
  - namespace: staging
    server: localhost:8080
  sourceRepos:
  - '*'
`
					if string(content) != expected {
						t.Fatalf("unexpected argocd manifest:\nexpected:\n%s\n\nactual:\n%s", expected, string(content))
					}
				}

				{

					content, err := util.ReadFile(s.Filesystem, "argocd/v1alpha1/production.yaml")
					if err != nil {
						t.Fatalf("unexpected error reading argocd manifest: %q", err)
					}
					expected := `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: production
spec:
  description: production
  destinations:
  - name: production
  sourceRepos:
  - '*'
`
					if string(content) != expected {
						t.Fatalf("unexpected argocd manifest:\nexpected:\n%s\n\nactual:\n%s", expected, string(content))
					}
				}
			},
		},
		{
			Name: "It creates an ArgoCd AppProject With Sync Windows",
			Transformers: []Transformer{
				&CreateEnvironment{Environment: "staging", Config: config.EnvironmentConfig{
					ArgoCd: &config.EnvironmentConfigArgoCd{
						Destination: config.ArgoCdDestination{
							Namespace: ptr.FromString("not-staging"),
							Server:    "localhost:8080",
						},
						SyncWindows: []config.ArgoCdSyncWindow{
							{
								Schedule: "* * * * *",
								Duration: "1h",
								Kind:     "deny",
							},
						},
					},
				}},
			},
			Test: func(t *testing.T, s *State) {
				// check that the state reads the correct versions
				content, err := util.ReadFile(s.Filesystem, "argocd/v1alpha1/staging.yaml")
				if err != nil {
					t.Fatalf("unexpected error reading argocd manifest: %q", err)
				}
				expected := `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: staging
spec:
  description: staging
  destinations:
  - namespace: not-staging
    server: localhost:8080
  sourceRepos:
  - '*'
  syncWindows:
  - applications:
    - '*'
    duration: 1h
    kind: deny
    manualSync: true
    schedule: '* * * * *'
`
				if string(content) != expected {
					t.Fatalf("unexpected argocd manifest:\nexpected:\n%s\n\nactual:\n%s", expected, string(content))
				}
			},
		},
		{
			Name: "It creates an ArgoCd AppProject With global resources",
			Transformers: []Transformer{
				&CreateEnvironment{Environment: "staging", Config: config.EnvironmentConfig{
					ArgoCd: &config.EnvironmentConfigArgoCd{
						Destination: config.ArgoCdDestination{
							Namespace: ptr.FromString("not-staging"),
							Server:    "localhost:8080",
						},
						ClusterResourceWhitelist: []config.AccessEntry{
							{
								Group: "*",
								Kind:  "MyClusterWideResource",
							},
							{
								Group: "*",
								Kind:  "ClusterSecretStore",
							},
						},
					},
				}},
			},
			Test: func(t *testing.T, s *State) {
				// check that the state reads the correct versions
				content, err := util.ReadFile(s.Filesystem, "argocd/v1alpha1/staging.yaml")
				if err != nil {
					t.Fatalf("unexpected error reading argocd manifest: %q", err)
				}
				expected := `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: staging
spec:
  clusterResourceWhitelist:
  - group: '*'
    kind: MyClusterWideResource
  - group: '*'
    kind: ClusterSecretStore
  description: staging
  destinations:
  - namespace: not-staging
    server: localhost:8080
  sourceRepos:
  - '*'
`
				if string(content) != expected {
					t.Fatalf("unexpected argocd manifest:\ndiff:\n%s\n\n", godebug.Diff(expected, string(content)))
				}
			},
		},
		{
			Name: "It creates ArgoCd Applications",
			Transformers: []Transformer{
				&CreateEnvironment{Environment: "staging", Config: config.EnvironmentConfig{
					ArgoCd: &config.EnvironmentConfigArgoCd{
						Destination: config.ArgoCdDestination{
							Namespace: ptr.FromString("staging"),
							Server:    "localhost:8080",
						},
					},
					Upstream: &config.EnvironmentConfigUpstream{
						Latest: true,
					},
				}},
				&CreateApplicationVersion{
					Application: "test",
					Manifests: map[string]string{
						"staging": "stagingmanifest",
					},
					Team:            "team1",
					WriteCommitData: true,
				},
				&CreateApplicationVersion{
					Application: "test2",
					Manifests: map[string]string{
						"staging": "stagingmanifest",
					},
					Team:            "team2",
					WriteCommitData: true,
				},
			},
			Test: func(t *testing.T, s *State) {
				// check that the state reads the correct versions
				content, err := util.ReadFile(s.Filesystem, "argocd/v1alpha1/staging.yaml")
				if err != nil {
					t.Fatalf("unexpected error reading argocd manifest: %q", err)
				}
				// The repository URL changes every time because the repository is in a tmp dir.
				repoURL := regexp.MustCompile(`repoURL: ([^\n]+)\n`).FindStringSubmatch(string(content))[1]
				expected := fmt.Sprintf(`apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: staging
spec:
  description: staging
  destinations:
  - namespace: staging
    server: localhost:8080
  sourceRepos:
  - '*'
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  annotations:
    argocd.argoproj.io/manifest-generate-paths: /environments/staging/applications/test/manifests
    com.freiheit.kuberpult/application: test
    com.freiheit.kuberpult/environment: staging
    com.freiheit.kuberpult/team: team1
  finalizers:
  - resources-finalizer.argocd.argoproj.io
  labels:
    com.freiheit.kuberpult/team: team1
  name: staging-test
spec:
  destination:
    namespace: staging
    server: localhost:8080
  project: staging
  source:
    path: environments/staging/applications/test/manifests
    repoURL: %s
    targetRevision: master
  syncPolicy:
    automated:
      allowEmpty: true
      prune: true
      selfHeal: true
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  annotations:
    argocd.argoproj.io/manifest-generate-paths: /environments/staging/applications/test2/manifests
    com.freiheit.kuberpult/application: test2
    com.freiheit.kuberpult/environment: staging
    com.freiheit.kuberpult/team: team2
  finalizers:
  - resources-finalizer.argocd.argoproj.io
  labels:
    com.freiheit.kuberpult/team: team2
  name: staging-test2
spec:
  destination:
    namespace: staging
    server: localhost:8080
  project: staging
  source:
    path: environments/staging/applications/test2/manifests
    repoURL: %s
    targetRevision: master
  syncPolicy:
    automated:
      allowEmpty: true
      prune: true
      selfHeal: true
`, repoURL, repoURL)
				if string(content) != expected {
					t.Fatalf("unexpected argocd manifest:\n%s", godebug.Diff(expected, string(content)))
				}
			},
		},
		{
			Name: "It creates ArgoCd Applications with labels",
			Transformers: []Transformer{
				&CreateEnvironment{Environment: "staging", Config: config.EnvironmentConfig{
					ArgoCd: &config.EnvironmentConfigArgoCd{
						Destination: config.ArgoCdDestination{
							Namespace: ptr.FromString("staging"),
							Server:    "localhost:8080",
						},
						ApplicationAnnotations: map[string]string{
							"b": "foo",
							"a": "bar",
						},
					},
					Upstream: &config.EnvironmentConfigUpstream{
						Latest: true,
					},
				}},
				&CreateApplicationVersion{
					Application: "test",
					Manifests: map[string]string{
						"staging": "stagingmanifest",
					},
					WriteCommitData: true,
				},
			},
			Test: func(t *testing.T, s *State) {
				// check that the state reads the correct versions
				content, err := util.ReadFile(s.Filesystem, "argocd/v1alpha1/staging.yaml")
				if err != nil {
					t.Fatalf("unexpected error reading argocd manifest: %q", err)
				}
				// The repository URL changes every time because the repository is in a tmp dir.
				repoURL := regexp.MustCompile(`repoURL: ([^\n]+)\n`).FindStringSubmatch(string(content))[1]
				expected := fmt.Sprintf(`apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: staging
spec:
  description: staging
  destinations:
  - namespace: staging
    server: localhost:8080
  sourceRepos:
  - '*'
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  annotations:
    a: bar
    argocd.argoproj.io/manifest-generate-paths: /environments/staging/applications/test/manifests
    b: foo
    com.freiheit.kuberpult/application: test
    com.freiheit.kuberpult/environment: staging
    com.freiheit.kuberpult/team: ""
  finalizers:
  - resources-finalizer.argocd.argoproj.io
  labels:
    com.freiheit.kuberpult/team: ""
  name: staging-test
spec:
  destination:
    namespace: staging
    server: localhost:8080
  project: staging
  source:
    path: environments/staging/applications/test/manifests
    repoURL: %s
    targetRevision: master
  syncPolicy:
    automated:
      allowEmpty: true
      prune: true
      selfHeal: true
`, repoURL)
				if string(content) != expected {
					t.Fatalf("unexpected argocd manifest:\ndiff:\n%s\n\n", godebug.Diff(expected, string(content)))
				}
			},
		},
		{
			Name: "It creates ArgoCd Applications with ignore differences",
			Transformers: []Transformer{
				&CreateEnvironment{Environment: "staging", Config: config.EnvironmentConfig{
					ArgoCd: &config.EnvironmentConfigArgoCd{
						Destination: config.ArgoCdDestination{
							Namespace: ptr.FromString("staging"),
							Server:    "localhost:8080",
						},
						IgnoreDifferences: []config.ArgoCdIgnoreDifference{
							{
								Group: "apps",
								Kind:  "Deployment",
								JSONPointers: []string{
									"/spec/replicas",
								},
								JqPathExpressions: []string{
									".foo.bar",
								},
							},
						},
					},
					Upstream: &config.EnvironmentConfigUpstream{
						Latest: true,
					},
				}},
				&CreateApplicationVersion{
					Application: "test",
					Manifests: map[string]string{
						"staging": "stagingmanifest",
					},
					WriteCommitData: true,
				},
			},
			Test: func(t *testing.T, s *State) {
				// check that the state reads the correct versions
				content, err := util.ReadFile(s.Filesystem, "argocd/v1alpha1/staging.yaml")
				if err != nil {
					t.Fatalf("unexpected error reading argocd manifest: %q", err)
				}
				// The repository URL changes every time because the repository is in a tmp dir.
				repoURL := regexp.MustCompile(`repoURL: ([^\n]+)\n`).FindStringSubmatch(string(content))[1]
				expected := fmt.Sprintf(`apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: staging
spec:
  description: staging
  destinations:
  - namespace: staging
    server: localhost:8080
  sourceRepos:
  - '*'
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  annotations:
    argocd.argoproj.io/manifest-generate-paths: /environments/staging/applications/test/manifests
    com.freiheit.kuberpult/application: test
    com.freiheit.kuberpult/environment: staging
    com.freiheit.kuberpult/team: ""
  finalizers:
  - resources-finalizer.argocd.argoproj.io
  labels:
    com.freiheit.kuberpult/team: ""
  name: staging-test
spec:
  destination:
    namespace: staging
    server: localhost:8080
  ignoreDifferences:
  - group: apps
    jqPathExpressions:
    - .foo.bar
    jsonPointers:
    - /spec/replicas
    kind: Deployment
  project: staging
  source:
    path: environments/staging/applications/test/manifests
    repoURL: %s
    targetRevision: master
  syncPolicy:
    automated:
      allowEmpty: true
      prune: true
      selfHeal: true
`, repoURL)
				if string(content) != expected {
					t.Fatalf("unexpected argocd manifest:\ndiff:\n%s\n\n", godebug.Diff(expected, string(content)))
				}
			},
		},
		{
			Name:          "CreateEnvironment errors in bootstrap mode",
			BootstrapMode: true,
			Transformers: []Transformer{
				&CreateEnvironment{Environment: "production", Config: c1},
			},
			ErrorTest: func(t *testing.T, err error) {
				expectedError := "Cannot create or update configuration in bootstrap mode. Please update configuration in config map instead."
				if err.Error() != expectedError {
					t.Errorf("Expected error '%s', got '%s'", expectedError, err.Error())
				}
			},
		},
		{
			Name:          "CreateEnvironment does not error in bootstrap mode without configuration",
			BootstrapMode: true,
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "production",
				},
			},
			Test: func(t *testing.T, s *State) {},
		},
	}
	for _, tc := range tcs {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			remoteDir := path.Join(dir, "remote")
			localDir := path.Join(dir, "local")
			cmd := exec.Command("git", "init", "--bare", remoteDir)
			cmd.Start()
			cmd.Wait()
			repo, err := New(
				testutil.MakeTestContext(),
				RepositoryConfig{
					URL:            remoteDir,
					Path:           localDir,
					CommitterEmail: "kuberpult@freiheit.com",
					CommitterName:  "kuberpult",
					BootstrapMode:  tc.BootstrapMode,
				},
			)
			if err != nil {
				t.Fatal(err)
			}

			for i, tf := range tc.Transformers {
				ctxWithTime := WithTimeNow(testutil.MakeTestContext(), timeNowOld)
				err = repo.Apply(ctxWithTime, tf)
				if err != nil {
					if tc.ErrorTest != nil && i == len(tc.Transformers)-1 {
						tc.ErrorTest(t, err)
						return
					} else {
						t.Fatalf("error applying transformations %q: %s", tf, err.Error())
					}
				}
			}
			if tc.ErrorTest != nil {
				t.Fatalf("expected an error but got none")
			}
			tc.Test(t, repo.State())
		})
	}
}

func makeTransformersDeployTestEnvLock(lock api.LockBehavior) []Transformer {
	return []Transformer{
		&CreateEnvironment{Environment: "production"},
		&CreateApplicationVersion{
			Application: "test",
			Manifests: map[string]string{
				"production": "productionmanifest",
			},
			WriteCommitData: true,
		},
		&CreateEnvironmentLock{
			Environment: "production",
			Message:     "don't",
			LockId:      "manual",
		},
		&DeployApplicationVersion{
			Environment:   "production",
			Application:   "test",
			Version:       1,
			LockBehaviour: lock,
		},
	}
}

func makeTransformersDeployTestAppLock(lock api.LockBehavior) []Transformer {
	return []Transformer{
		&CreateEnvironment{Environment: "production"},
		&CreateApplicationVersion{
			Application: "test",
			Manifests: map[string]string{
				"production": "productionmanifest",
			},
			WriteCommitData: true,
		},
		&CreateEnvironmentApplicationLock{
			Environment: "production",
			Application: "test",
			Message:     "don't",
			LockId:      "manual",
		},
		&DeployApplicationVersion{
			Environment:   "production",
			Application:   "test",
			Version:       1,
			LockBehaviour: lock,
		},
	}
}

func makeTransformersTwoDeploymentsWriteToQueue(lockA api.LockBehavior, lockB api.LockBehavior) []Transformer {
	return []Transformer{
		&CreateEnvironment{Environment: "production"},
		&CreateApplicationVersion{
			Application: "test",
			Manifests: map[string]string{
				"production": "productionmanifest",
			},
			WriteCommitData: true,
		},
		&CreateApplicationVersion{
			Application: "test",
			Manifests: map[string]string{
				"production": "productionmanifest",
			},
			WriteCommitData: true,
		},
		&CreateEnvironmentLock{
			Environment: "production",
			Message:     "stop",
			LockId:      "test",
		},
		&DeployApplicationVersion{
			Environment:   "production",
			Application:   "test",
			Version:       1,
			LockBehaviour: lockA,
		},
		&DeployApplicationVersion{
			Environment:   "production",
			Application:   "test",
			Version:       2,
			LockBehaviour: lockB,
		},
	}
}

func makeTransformersDoubleLock(lock api.LockBehavior, unlockBoth bool) []Transformer {
	res := []Transformer{
		&CreateEnvironment{Environment: "production"},
		&CreateApplicationVersion{
			Application: "test",
			Manifests: map[string]string{
				"production": "productionmanifest",
			},
			WriteCommitData: true,
		},
		&CreateEnvironmentLock{
			Environment: "production",
			Message:     "stop",
			LockId:      "test",
		},
		&CreateEnvironmentApplicationLock{
			Environment: "production",
			Application: "test",
			LockId:      "test",
			Message:     "stop",
		},
		&DeployApplicationVersion{
			Environment:   "production",
			Application:   "test",
			Version:       1,
			LockBehaviour: lock,
		},
		&DeleteEnvironmentLock{
			Environment: "production",
			LockId:      "test",
		},
		// we still have an app lock here, so no deployment should happen!
	}
	if unlockBoth {
		res = append(res, &DeleteEnvironmentApplicationLock{
			Environment: "production",
			Application: "test",
			LockId:      "test",
		})
	}
	return res
}

func makeTransformersForDelete(numVersions uint64) []Transformer {
	res := []Transformer{
		&CreateEnvironment{Environment: envProduction},
	}
	var v uint64
	for v = 1; v <= numVersions; v++ {
		res = append(res, &CreateApplicationVersion{
			Application: "test",
			Manifests: map[string]string{
				envProduction: "productionmanifest",
			},
			WriteCommitData: true,
		})
		res = append(res, &DeployApplicationVersion{
			Environment:   envProduction,
			Application:   "test",
			Version:       v,
			LockBehaviour: api.LockBehavior_FAIL,
		})
	}
	return res
}

func setupRepositoryTest(t *testing.T) Repository {
	repo, _ := setupRepositoryTestWithPath(t)
	return repo
}

func setupRepositoryTestWithPath(t *testing.T) (Repository, string) {
	dir := t.TempDir()
	remoteDir := path.Join(dir, "remote")
	localDir := path.Join(dir, "local")
	cmd := exec.Command("git", "init", "--bare", remoteDir)
	err := cmd.Start()
	if err != nil {
		t.Errorf("could not start git init")
		return nil, ""
	}
	err = cmd.Wait()
	if err != nil {
		t.Errorf("could not wait for git init to finish")
		return nil, ""
	}
	repo, err := New(
		testutil.MakeTestContext(),
		RepositoryConfig{
			URL:             remoteDir,
			Path:            localDir,
			CommitterEmail:  "kuberpult@freiheit.com",
			CommitterName:   "kuberpult",
			WriteCommitData: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return repo, remoteDir
}

// Injects an error in the filesystem of the state
type injectErr struct {
	Transformer
	collector *testfs.UsageCollector
	operation testfs.Operation
	filename  string
	err       error
}

func (i *injectErr) Transform(ctx context.Context, state *State) (string, *TransformerResult, error) {
	original := state.Filesystem
	state.Filesystem = i.collector.WithError(state.Filesystem, i.operation, i.filename, i.err)
	s, changes, err := i.Transformer.Transform(ctx, state)
	state.Filesystem = original
	return s, changes, err
}

func TestAllErrorsHandledDeleteEnvironmentLock(t *testing.T) {
	t.Parallel()
	collector := &testfs.UsageCollector{}
	tcs := []struct {
		name             string
		operation        testfs.Operation
		createLockBefore bool
		filename         string
		expectedError    string
	}{
		{
			name:             "delete lock succeeds",
			createLockBefore: true,
		},
		{
			name:             "delete lock fails",
			createLockBefore: true,
			operation:        testfs.REMOVE,
			filename:         "environments/dev/locks/foo",
			expectedError:    "failed to delete directory \"environments/dev/locks/foo\": obscure error",
		},
		{
			name:             "delete lock parent dir fails",
			createLockBefore: true,
			operation:        testfs.READDIR,
			filename:         "environments/dev/locks",
			expectedError:    "DeleteDirIfEmpty: failed to read directory \"environments/dev/locks\": obscure error",
		},
		{
			name:             "readdir fails on apps",
			createLockBefore: true,
			operation:        testfs.READDIR,
			filename:         "environments/dev/applications",
			expectedError:    "environment applications for \"dev\" not found: obscure error",
		},
		{
			name:             "readdir fails on locks",
			createLockBefore: true,
			operation:        testfs.READDIR,
			filename:         "environments/dev/locks",
			expectedError:    "DeleteDirIfEmpty: failed to read directory \"environments/dev/locks\": obscure error",
		},
		{
			name:             "stat fails on lock dir",
			createLockBefore: true,
			operation:        testfs.STAT,
			filename:         "environments/dev/locks/foo",
			expectedError:    "obscure error",
		},
		{
			name:             "remove fails on locks",
			createLockBefore: true,
			operation:        testfs.REMOVE,
			filename:         "environments/dev/locks",
			expectedError:    "DeleteDirIfEmpty: failed to delete directory \"environments/dev/locks\": obscure error",
		},
		{
			name:             "remove fails when lock does not exist",
			createLockBefore: false,
			operation:        testfs.REMOVE,
			filename:         "environments/dev/locks",
			expectedError:    "rpc error: code = FailedPrecondition desc = error: directory environments/dev/locks/foo for env lock does not exist",
		},
	}
	for _, tc := range tcs {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			repo := setupRepositoryTest(t)
			env := "dev"
			lockId := "foo"
			createLock := &CreateEnvironmentLock{
				Environment: env,
				LockId:      lockId,
				Message:     "",
			}
			ts := []Transformer{
				&CreateEnvironment{
					Environment: env,
				},
			}
			if tc.createLockBefore {
				ts = append(ts, createLock)
			}
			err := repo.Apply(testutil.MakeTestContext(), ts...)
			if err != nil {
				t.Fatal(err)
			}
			err = repo.Apply(testutil.MakeTestContext(), &injectErr{
				Transformer: &DeleteEnvironmentLock{
					Environment:    env,
					LockId:         lockId,
					Authentication: Authentication{RBACConfig: auth.RBACConfig{DexEnabled: false}},
				},
				collector: collector,
				operation: tc.operation,
				filename:  tc.filename,
				err:       fmt.Errorf("obscure error"),
			})

			if tc.expectedError != "" {
				if err == nil {
					t.Fatalf("expected error %q, but got nil", tc.expectedError)
				}
				actualErr := err.Error()
				if diff := cmp.Diff(tc.expectedError, actualErr); diff != "" {
					t.Errorf("Error mismatch (-want +got):\n%s", diff)
				}
			} else {
				if err != nil {
					t.Errorf("expected no error, but got %q", err)
				}
			}
		})
	}
	// Note: We have to run this after all tests in the array, in order to collect all untested operations:
	untested := collector.UntestedOps()
	for _, op := range untested {
		t.Errorf("Untested operations %s %s", op.Operation, op.Filename)
	}
}

func TestAllErrorsHandledDeleteEnvironmentApplicationLock(t *testing.T) {
	t.Parallel()
	collector := &testfs.UsageCollector{}
	tcs := []struct {
		name             string
		createLockBefore bool
		operation        testfs.Operation
		filename         string
		expectedError    string
	}{
		{
			name:             "delete lock succeeds",
			createLockBefore: true,
		},
		{
			name:             "delete lock fails - remove",
			createLockBefore: true,
			operation:        testfs.REMOVE,
			filename:         "environments/dev/applications/bar/locks/foo",
			expectedError:    "failed to delete directory \"environments/dev/applications/bar/locks/foo\": obscure error",
		},
		{
			name:             "delete lock fails - readdir",
			createLockBefore: true,
			operation:        testfs.READDIR,
			filename:         "environments/dev/applications/bar/locks",
			expectedError:    "DeleteDirIfEmpty: failed to read directory \"environments/dev/applications/bar/locks\": obscure error",
		},
		{
			name:             "stat queue fails",
			createLockBefore: true,
			operation:        testfs.READLINK,
			filename:         "environments/dev/applications/bar/queued_version",
			expectedError:    "failed reading symlink \"environments/dev/applications/bar/queued_version\": obscure error",
		},
		{
			name:             "stat queue fails 2",
			createLockBefore: true,
			operation:        testfs.STAT,
			filename:         "environments/dev/applications/bar/locks/foo",
			expectedError:    "obscure error",
		},
		{
			name:             "remove fails 2",
			createLockBefore: true,
			operation:        testfs.REMOVE,
			filename:         "environments/dev/applications/bar/locks",
			expectedError:    "DeleteDirIfEmpty: failed to delete directory \"environments/dev/applications/bar/locks\": obscure error",
		},
	}
	for _, tc := range tcs {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			repo := setupRepositoryTest(t)
			env := "dev"
			app := "bar"
			lockId := "foo"
			createLock := &CreateEnvironmentApplicationLock{
				Environment: env,
				Application: app,
				LockId:      lockId,
				Message:     "",
			}
			ts := []Transformer{
				&CreateEnvironment{
					Environment: env,
				},
			}
			if tc.createLockBefore {
				ts = append(ts, createLock)
			}
			err := repo.Apply(testutil.MakeTestContext(), ts...)
			if err != nil {
				t.Fatal(err)
			}
			err = repo.Apply(testutil.MakeTestContext(), &injectErr{
				Transformer: &DeleteEnvironmentApplicationLock{
					Environment: env,
					Application: app,
					LockId:      lockId,
				},
				collector: collector,
				operation: tc.operation,
				filename:  tc.filename,
				err:       fmt.Errorf("obscure error"),
			})
			if tc.expectedError != "" {
				if err == nil {
					t.Errorf("expected error to be %q but got <nil>", tc.expectedError)
				} else if err.Error() != tc.expectedError {
					t.Errorf("expected error to be %q but got %q", tc.expectedError, err)
				}
			} else {
				if err != nil {
					t.Errorf("expected no error, but got %q", err)
				}
			}
		})
	}
	// Note: We have to run this after all tests in the array, in order to collect all untested operations:
	untested := collector.UntestedOps()
	for _, op := range untested {
		t.Errorf("Untested operations %s %s", op.Operation, op.Filename)
	}
}

func mockSendMetrics(repo Repository, interval time.Duration) <-chan bool {
	ch := make(chan bool, 1)
	go RegularlySendDatadogMetrics(repo, interval, func(repo Repository) { ch <- true })
	return ch
}

func TestSendRegularlyDatadogMetrics(t *testing.T) {
	tcs := []struct {
		Name          string
		shouldSucceed bool
	}{
		{
			Name: "Testing ticker",
		},
	}
	for _, tc := range tcs {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			repo := setupRepositoryTest(t)

			select {
			case <-mockSendMetrics(repo, 1):
			case <-time.After(4 * time.Second):
				t.Fatal("An error occurred during the go routine")
			}

		})
	}
}

type MockClient struct {
	events []*statsd.Event
	statsd.ClientInterface
}

func (c *MockClient) Event(e *statsd.Event) error {
	if c == nil {
		return errors.New("no client provided")
	}
	c.events = append(c.events, e)
	return nil
}

func (c *MockClient) Gauge(_ string, _ float64, _ []string, _ float64) error {
	return nil
}

// Verify that MockClient implements the ClientInterface.
// https://golang.org/doc/faq#guarantee_satisfies_interface
var _ statsd.ClientInterface = &MockClient{}

func TestUpdateDatadogMetrics(t *testing.T) {
	tcs := []struct {
		Name          string
		Transformers  []Transformer
		expectedError string
		shouldSucceed bool
	}{
		{
			Name: "Application Lock metric is sent",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "acceptance",
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: envAcceptance, Latest: true}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envAcceptance: "acceptance",
					},
					WriteCommitData: true,
				},
				&CreateEnvironmentApplicationLock{
					Environment: "acceptance",
					Application: "app1",
					LockId:      "22133",
					Message:     "test",
				},
			},
			shouldSucceed: true,
		},
		{
			Name: "Application Lock metric is sent",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "acceptance",
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Environment: envAcceptance, Latest: true}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envAcceptance: "acceptance",
					},
					WriteCommitData: true,
				},
				&CreateEnvironmentLock{
					Environment: "acceptance",
					LockId:      "22133",
					Message:     "test",
				},
			},
			shouldSucceed: true,
		},
	}
	for _, tc := range tcs {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			repo := setupRepositoryTest(t)
			_, _, _, err := repo.ApplyTransformersInternal(testutil.MakeTestContext(), tc.Transformers...)

			if err != nil {
				t.Fatalf("Got an unexpected error: %v", err)
			}

			if tc.shouldSucceed {
				if err != nil {
					t.Fatalf("Expected no error: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("Expected an error but got none")
				} else {
					actualMsg := err.Error()
					if actualMsg != tc.expectedError {
						t.Fatalf("expected a different error.\nExpected: %q\nGot %q", tc.expectedError, actualMsg)
					}
				}
			}
		})
	}
}

func TestUpdateDatadogMetricsInternal(t *testing.T) {
	tcs := []struct {
		Name           string
		changes        *TransformerResult
		expectedError  string
		expectedEvents []statsd.Event
	}{
		{
			Name: "Changes are sent as events",
			changes: &TransformerResult{
				ChangedApps: []AppEnv{
					{
						App:  "app1",
						Env:  "envB",
						Team: "teamT",
					},
				},
				DeletedRootApps: nil,
				Commits:         nil,
			},
			expectedEvents: []statsd.Event{
				{
					Title:          "Kuberpult app deployed",
					Text:           "Kuberpult has deployed app1 to envB for team teamT",
					Timestamp:      time.Time{},
					Hostname:       "",
					AggregationKey: "",
					Priority:       "",
					SourceTypeName: "",
					AlertType:      "",
					Tags: []string{
						"kuberpult.application:app1",
						"kuberpult.environment:envB",
						"kuberpult.team:teamT",
					},
				},
			},
		},
		{
			Name: "2 Changes are sent as events",
			changes: &TransformerResult{
				ChangedApps: []AppEnv{
					{
						App:  "app1",
						Env:  "envB",
						Team: "teamT",
					},
					{
						App:  "app2",
						Env:  "envA",
						Team: "teamX",
					},
				},
				DeletedRootApps: nil,
				Commits:         nil,
			},
			expectedEvents: []statsd.Event{
				{
					Title:          "Kuberpult app deployed",
					Text:           "Kuberpult has deployed app1 to envB for team teamT",
					Timestamp:      time.Time{},
					Hostname:       "",
					AggregationKey: "",
					Priority:       "",
					SourceTypeName: "",
					AlertType:      "",
					Tags: []string{
						"kuberpult.application:app1",
						"kuberpult.environment:envB",
						"kuberpult.team:teamT",
					},
				},
				{
					Title:          "Kuberpult app deployed",
					Text:           "Kuberpult has deployed app2 to envA for team teamX",
					Timestamp:      time.Time{},
					Hostname:       "",
					AggregationKey: "",
					Priority:       "",
					SourceTypeName: "",
					AlertType:      "",
					Tags: []string{
						"kuberpult.application:app2",
						"kuberpult.environment:envA",
						"kuberpult.team:teamX",
					},
				},
			},
		},
	}
	for _, tc := range tcs {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			//t.Parallel() // do not run in parallel because of the global var `ddMetrics`!
			var mockClient = &MockClient{}
			var client statsd.ClientInterface = mockClient
			ddMetrics = client
			repo := setupRepositoryTest(t)

			err := UpdateDatadogMetrics(repo.State(), tc.changes)

			if err != nil {
				t.Fatalf("Expected no error: %v", err)
			}
			if len(tc.expectedEvents) != len(mockClient.events) {
				t.Fatalf("expected %d events, but got %d", len(tc.expectedEvents), len(mockClient.events))
			}
			for i := range tc.expectedEvents {
				var expectedEvent statsd.Event = tc.expectedEvents[i]
				var actualEvent statsd.Event = *mockClient.events[i]

				if diff := cmp.Diff(actualEvent, expectedEvent, cmpopts.IgnoreFields(statsd.Event{}, "Timestamp")); diff != "" {
					t.Errorf("got %v, want %v, diff (-want +got) %s", actualEvent, expectedEvent, diff)
				}
			}

		})
	}
}

func TestDeleteEnvFromApp(t *testing.T) {
	tcs := []struct {
		Name              string
		Transformers      []Transformer
		expectedError     string
		expectedCommitMsg string
		shouldSucceed     bool
	}{
		{
			Name: "Success",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: envProduction,
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Latest: true}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envProduction: "productionmanifest",
					},
					WriteCommitData: true,
				},
				&DeployApplicationVersion{
					Environment:   envProduction,
					Application:   "app1",
					Version:       1,
					LockBehaviour: api.LockBehavior_FAIL,
				},
				&DeleteEnvFromApp{
					Application: "app1",
					Environment: envProduction,
				},
			},
			expectedError:     "",
			expectedCommitMsg: "Environment 'production' was removed from application 'app1' successfully.",
			shouldSucceed:     true,
		},
		{
			Name: "Success Double Delete",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: envProduction,
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Latest: true}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envProduction: "productionmanifest",
					},
					WriteCommitData: true,
				},
				&DeployApplicationVersion{
					Environment:   envProduction,
					Application:   "app1",
					Version:       1,
					LockBehaviour: api.LockBehavior_FAIL,
				},
				&DeleteEnvFromApp{
					Application: "app1",
					Environment: envProduction,
				},
				&DeleteEnvFromApp{
					Application: "app1",
					Environment: envProduction,
				},
			},
			expectedError:     "",
			expectedCommitMsg: "Attempted to remove environment 'production' from application 'app1' but it did not exist.",
			shouldSucceed:     true,
		},
		{
			Name: "fail to provide app name",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: envProduction,
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Latest: true}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envProduction: "productionmanifest",
					},
					WriteCommitData: true,
				},
				&DeployApplicationVersion{
					Environment:   envProduction,
					Application:   "app1",
					Version:       1,
					LockBehaviour: api.LockBehavior_FAIL,
				},
				&DeleteEnvFromApp{
					Environment: envProduction,
				},
			},
			expectedError:     "DeleteEnvFromApp app '' on env 'production': Need to provide the application",
			expectedCommitMsg: "",
			shouldSucceed:     false,
		},
		{
			Name: "fail to provide env name",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: envProduction,
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Latest: true}},
				},
				&CreateApplicationVersion{
					Application: "app1",
					Manifests: map[string]string{
						envProduction: "productionmanifest",
					},
					WriteCommitData: true,
				},
				&DeployApplicationVersion{
					Environment:   envProduction,
					Application:   "app1",
					Version:       1,
					LockBehaviour: api.LockBehavior_FAIL,
				},
				&DeleteEnvFromApp{
					Application: "app1",
				},
			},
			expectedError:     "DeleteEnvFromApp app 'app1' on env '': Need to provide the environment",
			expectedCommitMsg: "",
			shouldSucceed:     false,
		},
	}
	for _, tc := range tcs {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			repo := setupRepositoryTest(t)
			commitMsg, _, _, err := repo.ApplyTransformersInternal(testutil.MakeTestContext(), tc.Transformers...)
			// note that we only check the LAST error here:
			if tc.shouldSucceed {
				if err != nil {
					t.Fatalf("Expected no error: %v", err)
				}
				actualMsg := commitMsg[len(commitMsg)-1]
				if actualMsg != tc.expectedCommitMsg {
					t.Fatalf("expected a different message.\nExpected: %q\nGot %q", tc.expectedCommitMsg, actualMsg)
				}
			} else {
				if err == nil {
					t.Fatalf("Expected an error but got none")
				} else {
					actualMsg := err.Error()
					if actualMsg != tc.expectedError {
						t.Fatalf("expected a different error.\nExpected: %q\nGot %q", tc.expectedError, actualMsg)
					}
				}
			}
		})
	}
}

func TestDeleteLocks(t *testing.T) {
	tcs := []struct {
		Name              string
		Transformers      []Transformer
		expectedError     string
		expectedCommitMsg string
		shouldSucceed     bool
	}{
		{
			Name: "Success delete env lock",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: envProduction,
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Latest: true}},
				},
				&CreateEnvironmentLock{
					Environment: envProduction,
					LockId:      "l123",
				},
				&DeleteEnvironmentLock{
					Environment: envProduction,
					LockId:      "l123",
				},
			},
			expectedError:     "",
			expectedCommitMsg: "Deleted lock \"l123\" on environment \"production\"",
			shouldSucceed:     true,
		},
		{
			Name: "Success delete app lock",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: envProduction,
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Latest: true}},
				},
				&CreateEnvironmentApplicationLock{
					Environment: envProduction,
					Application: "app1",
					LockId:      "l123",
					Message:     "none",
				},
				&DeleteEnvironmentApplicationLock{
					Environment: envProduction,
					Application: "app1",
					LockId:      "l123",
				},
			},
			expectedError:     "",
			expectedCommitMsg: "Deleted lock \"l123\" on environment \"production\" for application \"app1\"",
			shouldSucceed:     true,
		},
		{
			Name: "Success create env lock",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: envProduction,
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Latest: true}},
				},
				&CreateEnvironmentLock{
					Environment: envProduction,
					LockId:      "l123",
					Message:     "my lock",
				},
			},
			expectedError:     "",
			expectedCommitMsg: "Created lock \"l123\" on environment \"production\"",
			shouldSucceed:     true,
		},
		{
			Name: "Success create app lock",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: envProduction,
					Config:      config.EnvironmentConfig{Upstream: &config.EnvironmentConfigUpstream{Latest: true}},
				},
				&CreateEnvironmentApplicationLock{
					Environment: envProduction,
					Application: "app1",
					LockId:      "l123",
					Message:     "my lock",
				},
			},
			expectedError:     "",
			expectedCommitMsg: "Created lock \"l123\" on environment \"production\" for application \"app1\"",
			shouldSucceed:     true,
		},
	}
	for _, tc := range tcs {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			repo := setupRepositoryTest(t)
			commitMsg, _, _, err := repo.ApplyTransformersInternal(testutil.MakeTestContext(), tc.Transformers...)
			// note that we only check the LAST error here:
			if tc.shouldSucceed {
				if err != nil {
					t.Fatalf("Expected no error: %v", err)
				}
				actualMsg := commitMsg[len(commitMsg)-1]
				if actualMsg != tc.expectedCommitMsg {
					t.Fatalf("expected a different message.\nExpected: %q\nGot %q", tc.expectedCommitMsg, actualMsg)
				}
			} else {
				if err == nil {
					t.Fatalf("Expected an error but got none")
				} else {
					actualMsg := err.Error()
					if actualMsg != tc.expectedError {
						t.Fatalf("expected a different error.\nExpected: %q\nGot %q", tc.expectedError, actualMsg)
					}
				}
			}
		})
	}
}

func TestEnvironmentGroupLocks(t *testing.T) {
	group := ptr.FromString("prod")
	tcs := []struct {
		Name              string
		Transformers      []Transformer
		expectedError     string
		expectedCommitMsg string
		shouldSucceed     bool
	}{
		{
			Name: "Success create env group lock",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "prod-ca",
					Config:      testutil.MakeEnvConfigLatestWithGroup(nil, group),
				},
				&CreateEnvironment{
					Environment: "prod-de",
					Config:      testutil.MakeEnvConfigLatestWithGroup(nil, group),
				},
				&CreateEnvironment{
					Environment: "staging",
					Config:      testutil.MakeEnvConfigLatestWithGroup(nil, ptr.FromString("another-group")),
				},
				&CreateEnvironmentGroupLock{
					Authentication:   Authentication{},
					EnvironmentGroup: *group,
					LockId:           "my-lock",
					Message:          "my-message",
				},
			},
			expectedError:     "",
			expectedCommitMsg: "Creating locks 'my-lock' for environment group 'prod':\nCreated lock \"my-lock\" on environment \"prod-ca\"\nCreated lock \"my-lock\" on environment \"prod-de\"",
			shouldSucceed:     true,
		},
		{
			Name: "Success delete env group lock",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "prod-ca",
					Config:      testutil.MakeEnvConfigLatestWithGroup(nil, group),
				},
				&CreateEnvironment{
					Environment: "prod-de",
					Config:      testutil.MakeEnvConfigLatestWithGroup(nil, group),
				},
				&CreateEnvironment{
					Environment: "staging",
					Config:      testutil.MakeEnvConfigLatestWithGroup(nil, ptr.FromString("another-group")),
				},
				&CreateEnvironmentGroupLock{
					Authentication:   Authentication{},
					EnvironmentGroup: *group,
					LockId:           "my-lock",
					Message:          "my-message",
				},
				&DeleteEnvironmentGroupLock{
					Authentication:   Authentication{},
					EnvironmentGroup: *group,
					LockId:           "my-lock",
				},
			},
			expectedError:     "",
			expectedCommitMsg: "Deleting locks 'my-lock' for environment group 'prod':\nDeleted lock \"my-lock\" on environment \"prod-ca\"\nDeleted lock \"my-lock\" on environment \"prod-de\"",
			shouldSucceed:     true,
		},
		{
			Name: "Success delete env group that was created as env lock",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "prod-ca",
					Config:      testutil.MakeEnvConfigLatestWithGroup(nil, group),
				},
				&CreateEnvironmentLock{
					Authentication: Authentication{},
					Environment:    "prod-ca",
					LockId:         "my-lock",
					Message:        "my-message",
				},
				&DeleteEnvironmentGroupLock{
					Authentication:   Authentication{},
					EnvironmentGroup: *group,
					LockId:           "my-lock",
				},
			},
			expectedError:     "",
			expectedCommitMsg: "Deleting locks 'my-lock' for environment group 'prod':\nDeleted lock \"my-lock\" on environment \"prod-ca\"",
			shouldSucceed:     true,
		},
		{
			Name: "Success delete env lock that was created as env group lock",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "prod-ca",
					Config:      testutil.MakeEnvConfigLatestWithGroup(nil, group),
				},
				&CreateEnvironmentGroupLock{
					Authentication:   Authentication{},
					EnvironmentGroup: *group,
					LockId:           "my-lock",
					Message:          "my-message",
				},
				&DeleteEnvironmentLock{
					Authentication: Authentication{},
					Environment:    "prod-ca",
					LockId:         "my-lock",
				},
			},
			expectedError:     "",
			expectedCommitMsg: "Deleted lock \"my-lock\" on environment \"prod-ca\"",
			shouldSucceed:     true,
		},
		{
			Name: "Failure create env group lock - no envs found",
			Transformers: []Transformer{
				&CreateEnvironment{
					Environment: "prod-ca",
					Config:      testutil.MakeEnvConfigLatestWithGroup(nil, group),
				},
				&CreateEnvironment{
					Environment: "prod-de",
					Config:      testutil.MakeEnvConfigLatestWithGroup(nil, group),
				},
				&CreateEnvironment{
					Environment: "staging",
					Config:      testutil.MakeEnvConfigLatestWithGroup(nil, ptr.FromString("another-group")),
				},
				&CreateEnvironmentGroupLock{
					Authentication:   Authentication{},
					EnvironmentGroup: "dev",
					LockId:           "my-lock",
					Message:          "my-message",
				},
			},
			expectedError:     "rpc error: code = InvalidArgument desc = error: No environment found with given group 'dev'",
			expectedCommitMsg: "",
			shouldSucceed:     false,
		},
	}
	for _, tc := range tcs {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			repo := setupRepositoryTest(t)
			commitMsg, _, _, err := repo.ApplyTransformersInternal(testutil.MakeTestContext(), tc.Transformers...)
			// note that we only check the LAST error here:
			if tc.shouldSucceed {
				if err != nil {
					t.Fatalf("Expected no error: %v", err)
				}
				actualMsg := commitMsg[len(commitMsg)-1]
				if diff := cmp.Diff(actualMsg, tc.expectedCommitMsg); diff != "" {
					t.Errorf("got %v, want %v, diff (-want +got) %s", actualMsg, tc.expectedCommitMsg, diff)
				}
			} else {
				if err == nil {
					t.Fatalf("Expected an error but got none")
				} else {
					actualMsg := err.Error()
					if actualMsg != tc.expectedError {
						t.Fatalf("expected a different error.\nExpected: %q\nGot %q", tc.expectedError, actualMsg)
					}
				}
			}
		})
	}
}
