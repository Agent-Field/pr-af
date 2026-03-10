from __future__ import annotations

import os
import re
import subprocess

import httpx

from ..schemas.input import ChangedFile, GitHubPRData
from ..schemas.output import GitHubReview


class GitHubClient:
    def __init__(self, token: str | None = None):
        self.token = token or os.getenv("GITHUB_TOKEN", "")
        self.base_url = "https://api.github.com"

    @staticmethod
    def parse_pr_url(url: str) -> tuple[str, str, int]:
        """Extract owner, repo, PR number from GitHub PR URL."""
        match = re.match(r"https?://github\.com/([^/]+)/([^/]+)/pull/(\d+)", url)
        if not match:
            raise ValueError(f"Invalid GitHub PR URL: {url}")
        return match.group(1), match.group(2), int(match.group(3))

    def _headers(self) -> dict[str, str]:
        headers = {"Accept": "application/vnd.github+json"}
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"
        return headers

    async def fetch_pr(self, pr_url: str) -> GitHubPRData:
        """Fetch PR metadata, diff, and changed files from GitHub API."""
        owner, repo, number = self.parse_pr_url(pr_url)

        async with httpx.AsyncClient(timeout=30.0) as client:
            pr_resp = await client.get(
                f"{self.base_url}/repos/{owner}/{repo}/pulls/{number}",
                headers=self._headers(),
            )
            pr_resp.raise_for_status()
            pr_data = pr_resp.json()

            changed_files: list[ChangedFile] = []
            page = 1
            while True:
                files_resp = await client.get(
                    f"{self.base_url}/repos/{owner}/{repo}/pulls/{number}/files",
                    headers=self._headers(),
                    params={"per_page": 100, "page": page},
                )
                files_resp.raise_for_status()
                files_page = files_resp.json()
                if not files_page:
                    break

                for file_data in files_page:
                    changed_files.append(
                        ChangedFile(
                            path=file_data.get("filename", ""),
                            status=file_data.get("status", "modified"),
                            additions=file_data.get("additions", 0),
                            deletions=file_data.get("deletions", 0),
                            patch=file_data.get("patch", ""),
                            previous_path=file_data.get("previous_filename"),
                        )
                    )

                if len(files_page) < 100:
                    break
                page += 1

            commit_messages: list[str] = []
            commit_page = 1
            while True:
                commits_resp = await client.get(
                    f"{self.base_url}/repos/{owner}/{repo}/pulls/{number}/commits",
                    headers=self._headers(),
                    params={"per_page": 100, "page": commit_page},
                )
                commits_resp.raise_for_status()
                commits_data = commits_resp.json()
                if not commits_data:
                    break

                for commit in commits_data:
                    message = commit.get("commit", {}).get("message", "")
                    if message:
                        commit_messages.append(message)

                if len(commits_data) < 100:
                    break
                commit_page += 1

            diff_headers = self._headers()
            diff_headers["Accept"] = "application/vnd.github.v3.diff"
            diff_resp = await client.get(
                f"{self.base_url}/repos/{owner}/{repo}/pulls/{number}",
                headers=diff_headers,
            )
            diff_resp.raise_for_status()

        return GitHubPRData(
            owner=owner,
            repo=repo,
            number=number,
            title=pr_data.get("title", ""),
            description=pr_data.get("body") or "",
            labels=[label.get("name", "") for label in pr_data.get("labels", []) if label.get("name")],
            author=pr_data.get("user", {}).get("login", ""),
            base_sha=pr_data.get("base", {}).get("sha", ""),
            head_sha=pr_data.get("head", {}).get("sha", ""),
            commit_messages=commit_messages,
            diff=diff_resp.text,
            changed_files=changed_files,
        )

    async def post_review(
        self,
        owner: str,
        repo: str,
        pr_number: int,
        review: GitHubReview,
        commit_sha: str = "",
    ) -> dict[str, object]:
        """Post a review with inline comments to a GitHub PR."""
        payload: dict[str, object] = {
            "body": review.body,
            "event": review.event,
        }
        if commit_sha:
            payload["commit_id"] = commit_sha

        if review.comments:
            payload["comments"] = [
                {
                    "path": comment.path,
                    "line": comment.line,
                    "side": comment.side,
                    "body": comment.body,
                }
                for comment in review.comments
                if comment.path and comment.line > 0
            ]

        print(
            f"[PR-AF] Posting review to {owner}/{repo}#{pr_number}: "
            f"event={review.event}, {len(review.comments)} comments, "
            f"commit_sha={commit_sha[:12] if commit_sha else 'none'}",
            flush=True,
        )

        async with httpx.AsyncClient(timeout=60.0) as client:
            response = await client.post(
                f"{self.base_url}/repos/{owner}/{repo}/pulls/{pr_number}/reviews",
                headers=self._headers(),
                json=payload,
            )
            if response.status_code >= 400:
                error_body = response.text
                print(f"[PR-AF] GitHub API error {response.status_code}: {error_body}", flush=True)
            response.raise_for_status()
            result = response.json()
            print(f"[PR-AF] Review posted successfully: id={result.get('id')}", flush=True)
            return result

    async def clone_repo(
        self,
        owner: str,
        repo: str,
        target_dir: str,
        shallow: bool = True,
    ) -> str:
        """Clone repository to local path. Returns the path."""
        token = os.getenv("GH_TOKEN") or self.token or os.getenv("GITHUB_TOKEN", "")
        if not token:
            raise ValueError("GitHub token is required for clone_repo")

        repo_url = f"https://{token}@github.com/{owner}/{repo}.git"
        command = ["git", "clone"]
        if shallow:
            command.extend(["--depth", "1"])
        command.extend([repo_url, target_dir])

        subprocess.run(command, check=True, capture_output=True, text=True)
        return target_dir
