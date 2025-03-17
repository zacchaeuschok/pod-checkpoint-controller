# Container Checkpoint System - Sample Files

This directory contains sample files to help you quickly get started with the Container Checkpoint System.

## Files Included

1. `sample-app.yaml` - A simple stateful application (counter) that runs on node1
2. `app-checkpoint.yaml` - A ContainerCheckpoint resource to checkpoint the sample application
3. `restored-app-node2.yaml` - A pod configuration that restores from the checkpoint on node2
4. `scheduled-checkpoint.yaml` - A CronJob that creates checkpoints on a schedule
5. `checkpoint-creator-rbac.yaml` - RBAC configuration for the scheduled checkpoint job

## Usage

Follow the steps in the main setup guide, then use these files in sequence:

1. Deploy the sample application on node1:
   ```
   kubectl apply -f samples/sample-app.yaml
   ```

2. Create a checkpoint:
   ```
   kubectl apply -f samples/app-checkpoint.yaml
   ```

3. Delete the original pod and restore on node2:
   ```
   kubectl delete -f samples/sample-app.yaml
   kubectl apply -f samples/restored-app-node2.yaml
   ```

4. (Optional) Set up scheduled checkpoints:
   ```
   kubectl apply -f samples/checkpoint-creator-rbac.yaml
   kubectl apply -f samples/scheduled-checkpoint.yaml
   ```

For more detailed instructions, refer to the main documentation in `docs/setup.md`.
