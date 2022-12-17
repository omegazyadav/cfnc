package compose

import (
	"github.com/balmanrawat/cfn-compose/cfn"
	"github.com/balmanrawat/cfn-compose/logger"
	"github.com/balmanrawat/cfn-compose/libs"
	"github.com/balmanrawat/cfn-compose/config"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"
)

// var colors []string = []string{log.Blue, log.Yellow, log.Green, log.Magenta, log.Cyan}

type Work struct {
	JobName    string
	Job        config.Job
	DryRun     bool
	DeployMode bool
	CfnManager cfn.CFNManager
}

type Result struct {
	JobName string
	Error   error
}

func Apply(cc config.ComposeConfig, logLevel int32, deployMode bool, dryRun bool) {
	ctx := context.Background()
	ctx, cancelCtx := context.WithCancel(ctx)
	defer cancelCtx()

	logger.Start(logLevel)

	jobMap := make(map[int][]config.Job)
	for name, job := range cc.Jobs {
		job.Name = name
		jobs, ok := jobMap[job.Order]
		if ok {
			jobs = append(jobs, job)
			jobMap[job.Order] = jobs
		} else {
			jobMap[job.Order] = []config.Job{job}
		}
	}

	workChan := make(chan Work)
	resultsChan := make(chan Result)

	//Generate the worker pool as pre the job counts
	jobCounts := len(cc.Jobs)
	for i := 0; i < jobCounts; i++ {
		go ExecuteJob(ctx, workChan, resultsChan, i)
	}

	// Exporting AWS_PROFILE and AWS_REGION got from config
	if val, ok := cc.Vars["AWS_PROFILE"]; ok {
		os.Setenv("AWS_PROFILE", val)
	}

	if val, ok := cc.Vars["AWS_REGION"]; ok {
		os.Setenv("AWS_REGION", val)
	}

	sess, err := libs.GetAWSSession()
	if err != nil {
		logger.Log.Errorf("Failed while creating AWS Session: %s\n", err.Error())
		os.Exit(1)
	}

	cm := cfn.CFNManager{Session: sess}

	var order int
	var orders []int
	for key, _ := range jobMap {
		orders = append(orders, key)
	}

	logger.Log.Infof("TOTAL JOB COUNT: %d\n", jobCounts)

	if deployMode {
		sort.Ints(orders)
	}else{
		sort.Sort(sort.Reverse(sort.IntSlice(orders))) //execute jobs in reverse order for delete
	}

	//Dispatch Jobs in order
	for _, order = range orders {
		jobs, ok := jobMap[order]
		if !ok {
			continue
		}

		for _, job := range jobs {
			workChan <- Work{JobName: job.Name, Job: job, DryRun: dryRun, DeployMode: deployMode, CfnManager: cm}
		}

		logger.Log.Infof("Dispatched Order: %d, JobCount: %d.\n", order, len(jobs))

		//wait for jobs in each order to complete
		for i := 0; i < len(jobs); i++ {
			r := <-resultsChan
			if r.Error != nil {
				cancelCtx()
				logger.Log.Infoln("Graceful wait for cancelled jobs")
				time.Sleep(time.Second * 10)
				logger.Log.Errorf("CFN compose failed. Error: %s", r.Error)
				return
			}
		}
		logger.Log.Infof("All Jobs completed for Dispatched Order: %d\n\n", order)
	}

	time.Sleep(time.Second * 2)
	logger.Log.Infoln("CFN Compose Successfully Completed!!")
}

func ExecuteJob(ctx context.Context, workChan chan Work, resultsChan chan Result, workerId int) {
	defer func() {
		logger.Log.Debugf("Worker: %d exiting...\n", workerId)
	}()

	for {
		select {
		case work := <-workChan:
			//sleeping from readability
			time.Sleep(time.Millisecond * 500)
			name := work.JobName
			job := work.Job
			dryRun := work.DryRun
			deployMode := work.DeployMode
			cm := work.CfnManager
			ctx := context.WithValue(ctx, "job", name)

			if deployMode {
				for i:= 0 ;i < len(job.Stacks); i++{
					stack := job.Stacks[i]
					ctx := context.WithValue(ctx, "stack", stack.StackName)
					var err error
					if dryRun {
						err = stack.ApplyDryRun(ctx, cm)
					} else {
						logger.Log.InfoCtxf(ctx, "Applying Change...")
						err = stack.ApplyChanges(ctx, cm)
					}
	
					if err != nil {
						errStr := fmt.Sprintf("[JOB: %s] [STACK: %s]. Error: %s\n", name, stack.StackName, err)
						logger.Log.Infoln(errStr)
						resultsChan <- Result{
							Error:   errors.New(errStr),
							JobName: name,
						}
						break
					}
				}
			}else {
				for i:= len(job.Stacks) - 1; i >= 0; i--{
					stack := job.Stacks[i]
					ctx := context.WithValue(ctx, "stack", stack.StackName)
					var err error
					if dryRun {
						err = stack.DestoryDryRun(ctx, cm)
					} else {
						logger.Log.InfoCtxf(ctx, "Destroying Stack...")
						err = stack.Destroy(ctx, cm)
					}
	
					if err != nil {
						errStr := fmt.Sprintf("[JOB: %s] [STACK: %s]. Error: %s\n", name, stack.StackName, err)
						logger.Log.Infoln(errStr)
						resultsChan <- Result{
							Error:   errors.New(errStr),
							JobName: name,
						}
						break
					}
				}
			}

			resultsChan <- Result{JobName: name}

		case <-ctx.Done():
			if err := ctx.Err(); err != nil {
				logger.Log.DebugCtxf(ctx, "Cancel signal received Worker: %d, Info: %s\n", workerId, err)
			}
			return
		}
	}
}
