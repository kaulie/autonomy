package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	autonomy "github.com/kaulie/autonomy/src"
)

// version is stamped by build.sh (-X main.version=$APP_VERSION).
var version = "dev"

func convertToContextContainer(entity ExternalEntity) (autonomy.ContextContainer, error) {
	var err error
	var domainType autonomy.TaskDomain
	var contextContainerType autonomy.ContextContainerType

	domainType, err = autonomy.ConvertToTaskDomain(entity.EntityDomain)
	if err != nil {
		return autonomy.ContextContainer{}, err
	}
	contextContainerType, err = autonomy.ConvertToContextContainerType(entity.EntityType)
	if err != nil {
		return autonomy.ContextContainer{}, err
	}

	return autonomy.ContextContainer{
		ID:                   entity.ID,
		Name:                 entity.Name,
		Description:          entity.Description,
		CreatedAt:            entity.CreatedAt,
		UpdatedAt:            entity.UpdatedAt,
		DomainType:           domainType,
		ContextContainerType: contextContainerType,
	}, nil
}

func main() {

	var err error
	var _autonomy *autonomy.Autonomy

	_autonomy, err = autonomy.BootstrapAutonomy()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer func() {
		if err := _autonomy.Close(); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}()

	// HTTP mode. scripts/start.sh sets AUTONOMY_HTTP_ADDR from SERVICE_PORT
	// (the deployment platform injects that; see scripts/start.sh).
	if addr := strings.TrimSpace(os.Getenv("AUTONOMY_HTTP_ADDR")); addr != "" {
		fmt.Fprintf(os.Stderr, "[autonomy] version=%s listen=%s\n", version, addr)
		srv := autonomy.NewHTTPServer(_autonomy)
		if err := srv.ListenAndServe(addr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	// init context entity
	projectExternalEntity := ExternalEntity{
		ID:           "project-2",
		Name:         "Project 2",
		Description:  "Project 2 description",
		EntityType:   "project",
		EntityDomain: "software_development",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	projectContextContainer, err := convertToContextContainer(projectExternalEntity)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	err = autonomy.RegisterContextContainer(projectContextContainer)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	sourceCodeEntity := autonomy.SourceCodeEntity{
		Meta: autonomy.Entity{
			ID:          "src-1",
			Name:        "source code",
			Description: "deployment project",
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		},
		Repository: autonomy.Repository{
			URL:        "https://github.com/kaulie/agent-control-plane-deployment",
			MainBranch: "main",
		},
	}
	serviceEntity := autonomy.ServiceEntity{
		Meta: autonomy.Entity{
			ID:          "service-1",
			Name:        "service",
			Description: "service description",
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		},
		ServiceId:   "agent-control-plane-deployment",
		ServiceName: "agent-control-plane-deployment",
	}
	err = autonomy.RegisterDomainEntity(serviceEntity)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	err = autonomy.RegisterDomainEntity(sourceCodeEntity)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	err = autonomy.BindEntityToContextContainer(sourceCodeEntity.Entity(), projectContextContainer)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	err = autonomy.BindEntityToContextContainer(serviceEntity.Entity(), projectContextContainer)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	task := &autonomy.Task{
		ID:          "task-28",
		Description: "开放服务契约的前端入口，提交commit，提PR,merge代码后部署上线",
		Status:      "pending",
		GoalType:    autonomy.GoalType_FEATURE,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		ContextRef: map[autonomy.ContextContainerType]string{
			autonomy.ContextContainerTypeProject: projectContextContainer.ID,
		},
	}

	err = _autonomy.Run(task)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
