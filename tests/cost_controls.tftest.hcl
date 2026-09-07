# Plans use a mock provider: no GCP credentials or cloud resources are needed.
mock_provider "google" {}

variables {
  project_id        = "demo-project-123"
  region            = "us-central1"
  zone              = "us-central1-a"
  deployment_source = "docker"
  docker_image      = "nginx:1.30.4"
}

run "limited_demo_stops_without_deleting" {
  command = plan

  variables {
    max_runtime_hours = 24
  }

  assert {
    condition     = google_compute_instance.demo.scheduling[0].max_run_duration[0].seconds == 86400
    error_message = "The configured 24-hour lifetime must reach the VM plan."
  }
  assert {
    condition     = google_compute_instance.demo.scheduling[0].instance_termination_action == "STOP" && google_compute_instance.demo.scheduling[0].automatic_restart == false
    error_message = "Expiry must stop the VM without deleting its data or auto-restarting it."
  }
  assert {
    condition     = google_compute_instance.demo.scheduling[0].provisioning_model == "STANDARD"
    error_message = "Runtime limits must not silently switch the VM to Spot."
  }
  assert {
    condition     = google_compute_instance.demo.network_interface[0].access_config[0].network_tier == "STANDARD"
    error_message = "The VM's external IPv4 must explicitly use Standard Tier."
  }
}

run "omitted_limit_keeps_existing_behavior" {
  command = plan

  assert {
    condition     = length(google_compute_instance.demo.scheduling) == 0
    error_message = "Omitting max_runtime_hours must not add a shutdown policy."
  }
}
