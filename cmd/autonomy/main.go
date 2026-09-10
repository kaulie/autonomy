package main

import (
	"fmt"
	"os"
	"time"

	autonomy "github.com/kaulie/autonomy/src"
)

func convertToContextEntity(entity ExternalEntity) (autonomy.ContextEntity, error) {
	var err error
	var domainType autonomy.TaskDomain
	var contextEntityType autonomy.ContextEntityType

	domainType, err = autonomy.ConvertToTaskDomain(entity.EntityDomain)
	if err != nil {
		return autonomy.ContextEntity{}, err
	}
	contextEntityType, err = autonomy.ConvertToContextEntityType(entity.EntityType)
	if err != nil {
		return autonomy.ContextEntity{}, err
	}

	return autonomy.ContextEntity{
		ID:                entity.ID,
		Name:              entity.Name,
		Description:       entity.Description,
		CreatedAt:         entity.CreatedAt,
		UpdatedAt:         entity.UpdatedAt,
		DomainType:        domainType,
		ContextEntityType: contextEntityType,
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
		ID:          "project-1",
		Name:        "Project 1",
		Description: "Project 1 description",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	projectContextEntity, err := convertToContextEntity(projectExternalEntity)

	err = autonomy.RegisterContextEntity(projectContextEntity)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	sourceCodeEntity := autonomy.SourceCodeEntity{
		Meta: autonomy.Entity{
			ID:          "src-1",
			Name:        "source code",
			Description: "source code",
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		},
	}

	err = autonomy.RegisterDomainEntity(sourceCodeEntity)
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
		ContextRef: map[autonomy.ContextEntityType]string{
			autonomy.ContextEntityTypeProject: projectContextEntity.ID,
		},
	}

	err = _autonomy.Run(task)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
