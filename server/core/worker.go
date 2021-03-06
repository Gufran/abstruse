package core

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/bleenco/abstruse/internal/auth"
	"github.com/bleenco/abstruse/internal/common"
	pb "github.com/bleenco/abstruse/pb"
	"github.com/bleenco/abstruse/server/config"
	"github.com/bleenco/abstruse/server/db/model"
	"github.com/bleenco/abstruse/server/registry"
	"github.com/golang/protobuf/ptypes/empty"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type (
	// Worker represents gRPC worker node client.
	Worker struct {
		mu      sync.Mutex
		id      string
		addr    string
		max     int
		running int
		host    HostInfo
		usage   []Usage
		conn    *grpc.ClientConn
		cli     pb.APIClient
		app     *App
		logger  *zap.SugaredLogger
	}

	// HostInfo holds host information about remote worker node.
	HostInfo struct {
		ID                   string    `json:"id"`
		Addr                 string    `json:"addr"`
		Hostname             string    `json:"hostname"`
		Uptime               uint64    `json:"uptime"`
		BootTime             uint64    `json:"bootTime"`
		Procs                uint64    `json:"procs"`
		Os                   string    `json:"os"`
		Platform             string    `json:"platform"`
		PlatformFamily       string    `json:"platformFamily"`
		PlatformVersion      string    `json:"platformVersion"`
		KernelVersion        string    `json:"kernelVersion"`
		KernelArch           string    `json:"kernelArch"`
		VirtualizationSystem string    `json:"virtualizationSystem"`
		VirtualizationRole   string    `json:"virtualizationRole"`
		HostID               string    `json:"hostID"`
		MaxParallel          uint64    `json:"maxParallel"`
		ConnectedAt          time.Time `json:"connectedAt"`
	}

	// Usage holds remote workers node usage information.
	Usage struct {
		CPU       int32     `json:"cpu"`
		Mem       int32     `json:"mem"`
		Max       int       `json:"jobsMax"`
		Running   int       `json:"jobsRunning"`
		Timestamp time.Time `json:"timestamp"`
	}
)

// NewWorker initializes new worker and returns it.
func NewWorker(id, addr string, cfg *config.Config, logger *zap.Logger, app *App) (*Worker, error) {
	if cfg.TLS.Cert == "" || cfg.TLS.Key == "" {
		return nil, fmt.Errorf("certificate and key must be specified")
	}

	grpcOpts := []grpc.DialOption{}
	certificate, err := tls.LoadX509KeyPair(cfg.TLS.Cert, cfg.TLS.Key)
	if err != nil {
		return nil, err
	}

	creds := credentials.NewTLS(&tls.Config{
		Certificates:       []tls.Certificate{certificate},
		InsecureSkipVerify: true,
	})
	jwt, err := auth.GenerateWorkerJWT(id)
	if err != nil {
		return nil, err
	}
	auth := &auth.Authentication{
		Identifier: id,
		JWT:        jwt,
	}

	grpcOpts = append(grpcOpts, grpc.WithTransportCredentials(creds))
	grpcOpts = append(grpcOpts, grpc.WithPerRPCCredentials(auth))

	conn, err := grpc.Dial(addr, grpcOpts...)
	if err != nil {
		return nil, err
	}
	cli := pb.NewAPIClient(conn)

	return &Worker{
		id:     id,
		addr:   addr,
		conn:   conn,
		cli:    cli,
		app:    app,
		logger: logger.With(zap.String("worker", id)).Sugar(),
	}, nil
}

// Run starts the worker.
func (w *Worker) Run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer w.conn.Close()

	host, err := w.hostInfo(ctx)
	if err != nil {
		return err
	}
	w.host = host
	w.max = int(w.host.MaxParallel)
	w.logger.Infof("connected to worker %s %s with capacity %d", w.id, w.addr, w.max)
	w.emitData()
	w.app.scheduler.AddWorker(w)

	ch := make(chan error)

	go func() {
		if err := w.usageStats(context.Background()); err != nil {
			ch <- err
		}
	}()

	err = <-ch
	w.logger.Infof("closed connection to worker %s %s", w.id, w.addr)
	return err
}

// SetCapacity sets current capacity.
func (w *Worker) SetCapacity(n int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.running = n
}

// Capacity returns current capacity.
func (w *Worker) Capacity() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return int(w.max - w.running)
}

// ID returns workers id.
func (w *Worker) ID() string {
	return w.id
}

// Addr returns remote address.
func (w *Worker) Addr() string {
	return w.addr
}

// Host returns host info.
func (w *Worker) Host() HostInfo {
	return w.host
}

// Usage returns worker usage.
func (w *Worker) Usage() []Usage {
	return w.usage
}

// usageStats gRPC stream.
func (w *Worker) usageStats(ctx context.Context) error {
	stream, err := w.cli.Usage(ctx)
	if err != nil {
		return err
	}
	defer stream.CloseSend()

	for {
		stats, err := stream.Recv()
		if err != nil {
			return err
		}
		usage := Usage{
			CPU:       stats.GetCpu(),
			Mem:       stats.GetMem(),
			Max:       w.max,
			Running:   w.running,
			Timestamp: time.Now(),
		}

		w.mu.Lock()
		w.usage = append(w.usage, usage)
		if len(w.usage) > 120 {
			// TODO save to db.
			w.usage = w.usage[len(w.usage)-120:]
		}
		w.emitUsage()
		w.mu.Unlock()
	}
}

// logOutput gRPC stream.
func (w *Worker) logOutput(ctx context.Context, jobID, buildID uint) error {
	job := &pb.Job{Id: uint64(jobID)}
	stream, err := w.cli.JobLog(ctx, job)
	if err != nil {
		return err
	}
	defer stream.CloseSend()

	for {
		output, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		id, log := uint(output.GetId()), string(output.GetContent())
		w.app.scheduler.mu.Lock()
		if j, ok := w.app.scheduler.pending[id]; ok {
			j.Log = append(j.Log, log)
		}
		w.app.scheduler.mu.Unlock()
		w.app.ws.App.Broadcast(fmt.Sprintf("/subs/logs/%d", id), map[string]interface{}{
			"id":  id,
			"log": log,
		})
	}
}

// hostInfo returns remote workers systme information.
func (w *Worker) hostInfo(ctx context.Context) (HostInfo, error) {
	info, err := w.cli.HostInfo(ctx, &empty.Empty{})
	return HostInfo{
		ID:                   info.GetId(),
		Addr:                 info.GetAddr(),
		Hostname:             info.GetHostname(),
		Uptime:               info.GetUptime(),
		BootTime:             info.GetBootTime(),
		Procs:                info.GetProcs(),
		Os:                   info.GetOs(),
		Platform:             info.GetPlatform(),
		PlatformFamily:       info.GetPlatformFamily(),
		PlatformVersion:      info.GetPlatformVersion(),
		KernelVersion:        info.GetKernelVersion(),
		KernelArch:           info.GetKernelArch(),
		VirtualizationSystem: info.GetVirtualizationSystem(),
		VirtualizationRole:   info.GetVirtualizationRole(),
		HostID:               info.GetHostID(),
		MaxParallel:          info.GetMaxParallel(),
		ConnectedAt:          time.Now(),
	}, err
}

// buildImage gRPC stream.
func (w *Worker) buildImage(ctx context.Context, name, dockerfile string, tags []string) error {
	image := &pb.Image{Name: name, Dockerfile: dockerfile, Tags: tags}
	stream, err := w.cli.BuildImage(ctx, image)
	if err != nil {
		return err
	}
	defer stream.CloseSend()

	type img struct {
		digest   string
		buildLog []string
		build    bool
		pushLog  []string
		push     bool
		err      error
	}

	result := make(map[string]*img)

loop:
	for {
		output, err := stream.Recv()
		if err != nil {
			break
		}

		name := output.GetName()
		tags := output.GetTags()
		content := string(output.GetContent())

		for _, tag := range tags {
			id := fmt.Sprintf("%s:%s", name, tag)
			if _, ok := result[id]; !ok {
				result[id] = &img{}
			}
		}

		switch status := output.GetStatus(); status {
		case pb.ImageOutput_BuildStream:
			for _, tag := range tags {
				id := fmt.Sprintf("%s:%s", name, tag)
				result[id].buildLog = append(result[id].buildLog, content)
			}
			w.emit("/subs/images", map[string]interface{}{
				"name":     name,
				"tags":     tags,
				"buildLog": content,
			})
		case pb.ImageOutput_PushStream:
			for _, tag := range tags {
				id := fmt.Sprintf("%s:%s", name, tag)
				result[id].pushLog = append(result[id].pushLog, content)
			}
			w.emit("/subs/images", map[string]interface{}{
				"name":    name,
				"tags":    tags,
				"pushLog": content,
			})
		case pb.ImageOutput_BuildOK:
			for _, tag := range tags {
				id := fmt.Sprintf("%s:%s", name, tag)
				result[id].build = true
			}
			w.emit("/subs/images", map[string]interface{}{
				"name":  name,
				"tags":  tags,
				"build": true,
			})
		case pb.ImageOutput_BuildError:
			for _, tag := range tags {
				id := fmt.Sprintf("%s:%s", name, tag)
				result[id].build = false
				result[id].err = fmt.Errorf(content)
				w.emit("/subs/images", map[string]interface{}{
					"name":  name,
					"tags":  tags,
					"build": false,
					"error": content,
				})
				break loop
			}
		case pb.ImageOutput_PushOK:
			splitted := strings.Split(content, " ")
			for _, tag := range tags {
				id := fmt.Sprintf("%s:%s", name, tag)
				result[id].push = true
				result[id].digest = splitted[2]
			}
			w.emit("/subs/images", map[string]interface{}{
				"name":   name,
				"tags":   tags,
				"push":   true,
				"digest": splitted[2],
			})
		case pb.ImageOutput_PushError:
			for _, tag := range tags {
				id := fmt.Sprintf("%s:%s", name, tag)
				result[id].push = false
				result[id].err = fmt.Errorf(content)
				w.emit("/subs/images", map[string]interface{}{
					"name":  name,
					"tags":  tags,
					"push":  false,
					"error": content,
				})
				break loop
			}
		}
	}

	client, err := registry.NewClient(common.RegistryURL, w.app.cfg.Registry.Username, w.app.cfg.Registry.Password)
	if err != nil {
		return err
	}

	for name, res := range result {
		if res.err != nil {
			continue
		}

		splitted := strings.Split(name, ":")
		name = splitted[0]
		tag := splitted[1]

		image := model.Image{Name: name}
		image, err = w.app.repo.Image.CreateOrUpdate(image)
		if err != nil {
			return err
		}

		imageTag := &model.ImageTag{
			Tag:        tag,
			Dockerfile: strings.TrimSpace(dockerfile),
			Digest:     strings.TrimSpace(res.digest),
			BuildLog:   strings.Join(res.buildLog, ""),
			PushLog:    strings.Join(res.pushLog, ""),
			ImageID:    image.ID,
			BuildTime:  time.Now(),
		}

		manifest, err := client.FindManifest(context.Background(), name, tag)
		if err != nil {
			return err
		}
		imageTag.Size = manifest.Size

		imageTag, err = w.app.repo.Image.CreateOrUpdateTag(imageTag)
		if err != nil {
			return err
		}
	}

	return nil
}

// emit broadcasts data via websocket.
func (w *Worker) emit(sub string, data map[string]interface{}) {
	w.app.ws.App.Broadcast(sub, data)
}

// emitData broadcast newly created worker via websocket.
func (w *Worker) emitData() {
	w.app.ws.App.Broadcast("/subs/workers_add", map[string]interface{}{
		"id":    w.id,
		"addr":  w.Addr(),
		"host":  w.Host(),
		"usage": w.Usage(),
	})
}

// emitUsage broadcast workers usage info.
func (w *Worker) emitUsage() {
	if len(w.usage) < 1 {
		return
	}
	usage := w.usage[len(w.usage)-1]
	w.app.ws.App.Broadcast("/subs/workers_usage", map[string]interface{}{
		"id":          w.id,
		"addr":        w.addr,
		"cpu":         usage.CPU,
		"mem":         usage.Mem,
		"jobsMax":     w.max,
		"jobsRunning": w.running,
		"timestamp":   time.Now(),
	})
}
