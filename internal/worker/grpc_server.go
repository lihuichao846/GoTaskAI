package worker

import (
	"context"
	"fmt"
	pb "gotaskai/api/proto/worker"
	"gotaskai/internal/model"
	"gotaskai/internal/queue"
	"log/slog"
)

type GrpcServer struct {
	pb.UnimplementedWorkerServiceServer
	manager *queue.TaskManager
}

func NewGrpcServer(manager *queue.TaskManager) *GrpcServer {
	return &GrpcServer{
		manager: manager,
	}
}

func (s *GrpcServer) CancelTask(ctx context.Context, req *pb.CancelRequest) (*pb.CancelResponse, error) {
	slog.Info("Received gRPC request to cancel task", "task_id", req.GetTaskId())
	
	task, ok := s.manager.GetTask(req.GetTaskId())
	if !ok {
		return &pb.CancelResponse{
			Success: false,
			Message: "Task not found",
		}, nil
	}

	if task.Status != model.StatusPending && task.Status != model.StatusProcessing {
		return &pb.CancelResponse{
			Success: false,
			Message: fmt.Sprintf("Task cannot be cancelled in current status: %s", task.Status),
		}, nil
	}

	// 强制将状态更新为取消
	task.Status = model.StatusCancelled
	task.Error = "Task was forcefully cancelled via gRPC"
	s.manager.UpdateTask(task)

	return &pb.CancelResponse{
		Success: true,
		Message: "Task successfully cancelled",
	}, nil
}
