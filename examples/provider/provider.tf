terraform {
  required_providers {
    cursor = {
      source  = "cursor/cursor"
      version = "~> 0.1"
    }
  }
}

provider "cursor" {
  # Or set the CURSOR_TOKEN environment variable.
  token = var.cursor_token
}
