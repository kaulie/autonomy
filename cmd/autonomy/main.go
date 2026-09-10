package main

import (
	"fmt"
	"os"
	"time"

	autonomy "github.com/kaulie/autonomy/src"
)

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

	// init context entity
	projectExternalEntity := ExternalEntity{
		ID:           "project-1",
		Name:         "Project 1",
		Description:  "Project 1 description",
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
			Description: "a agent autonomy project",
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		},
		Repository: autonomy.Repository{
			URL:        "https://github.com/kaulie/autonomy",
			MainBranch: "main",
		},
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

	task := &autonomy.Task{
		ID:          "task-1",
		Description: "Develop a new feature",
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
