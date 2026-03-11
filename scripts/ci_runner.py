#!/usr/bin/env python3
"""
CI Runner for PR-AF
Fires an async execution to the AgentField Control Plane and polls until completion.
Ensures GitHub Actions runners stay alive while the multi-agent DAG executes.
"""

import json
import os
import sys
import time
import urllib.error
import urllib.request

CP_URL = os.environ.get("AGENTFIELD_SERVER", "http://localhost:8080")

def main():
    pr_url = os.environ.get("PR_URL")
    if not pr_url:
        print("Error: PR_URL environment variable is required.")
        sys.exit(1)

    print(f"[CI] Initiating PR-AF Review for: {pr_url}")
    
    # 1. Fire the execution
    payload = json.dumps({
        "input": {
            "pr_url": pr_url,
            "depth": "standard",
            "dry_run": False
        }
    }).encode("utf-8")

    req = urllib.request.Request(
        f"{CP_URL}/api/v1/execute/async/pr-af.review",
        data=payload,
        headers={"Content-Type": "application/json"}
    )

    try:
        with urllib.request.urlopen(req) as response:
            res_data = json.loads(response.read().decode())
            exec_id = res_data.get("execution_id")
            if not exec_id:
                print("Error: Failed to get execution_id")
                sys.exit(1)
            print(f"[CI] Review dispatched. Execution ID: {exec_id}")
    except urllib.error.URLError as e:
        print(f"Error triggering review: {e}")
        sys.exit(1)

    # 2. Poll for completion
    print("[CI] Polling for completion (this may take 30-60 minutes)...")
    start_time = time.time()
    
    while True:
        time.sleep(30) # Poll every 30s
        elapsed_min = (time.time() - start_time) / 60
        
        status_req = urllib.request.Request(f"{CP_URL}/api/ui/v1/executions/{exec_id}/details")
        try:
            with urllib.request.urlopen(status_req) as response:
                status_data = json.loads(response.read().decode())
                status = status_data.get("status")
                
                print(f"[{elapsed_min:.1f}m] Status: {status}")
                
                if status == "succeeded":
                    print("\n[CI] Review completed successfully!")
                    sys.exit(0)
                elif status in ("failed", "cancelled"):
                    print(f"\n[CI] Review ended with status: {status}")
                    print(f"Error details: {status_data.get('error', 'None')}")
                    sys.exit(1)
        except urllib.error.URLError as e:
            print(f"[{elapsed_min:.1f}m] Warning: Could not reach Control Plane API: {e}")

if __name__ == "__main__":
    main()
