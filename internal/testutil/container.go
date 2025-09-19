package testutil

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type ContainerRuntime struct {
	name   string
	binary string
}

type ContainerRunOptions struct {
	Image      string
	Cmd        []string
	Env        []string
	Mounts     []ContainerMount
	WorkDir    string
	Privileged bool
	ExtraArgs  []string
}

type ContainerMount struct {
	Source   string
	Target   string
	ReadOnly bool
}

func FindContainerRuntime() (*ContainerRuntime, error) {
	candidates := []string{"docker", "podman"}
	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate)
		if err == nil {
			return &ContainerRuntime{name: candidate, binary: path}, nil
		}
	}
	return nil, fmt.Errorf("no container runtime found (looked for docker, podman)")
}

func (r *ContainerRuntime) Run(ctx context.Context, opts ContainerRunOptions) ([]byte, error) {
	if opts.Image == "" {
		return nil, fmt.Errorf("container image must be specified")
	}
	if len(opts.Cmd) == 0 {
		return nil, fmt.Errorf("container command must be specified")
	}

	args := []string{"run", "--rm"}
	if opts.Privileged {
		args = append(args, "--privileged")
	}
	for _, env := range opts.Env {
		if env != "" {
			args = append(args, "-e", env)
		}
	}
	for _, mount := range opts.Mounts {
		if mount.Source == "" || mount.Target == "" {
			return nil, fmt.Errorf("mounts must define both source and target paths")
		}
		src := mount.Source
		if !filepath.IsAbs(src) {
			abs, err := filepath.Abs(src)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve mount source %q: %w", src, err)
			}
			src = abs
		}
		if _, err := os.Stat(src); err != nil {
			return nil, fmt.Errorf("mount source %q not accessible: %w", src, err)
		}
		spec := fmt.Sprintf("%s:%s", src, mount.Target)
		if mount.ReadOnly {
			spec += ":ro"
		}
		args = append(args, "-v", spec)
	}
	if opts.WorkDir != "" {
		args = append(args, "-w", opts.WorkDir)
	}
	args = append(args, opts.ExtraArgs...)
	args = append(args, opts.Image)
	args = append(args, opts.Cmd...)

	cmd := exec.CommandContext(ctx, r.binary, args...) // #nosec G204 -- runtime is developer-controlled
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	err := cmd.Run()
	return combined.Bytes(), err
}

func (r *ContainerRuntime) Name() string {
	return r.name
}
