# 🤖 AI Review Agent

<div align="center">
  <img src="https://img.shields.io/badge/go-1.25.5-00ADD8?style=for-the-badge&logo=go&logoColor=white" alt="Go Version">
  <img src="https://img.shields.io/badge/GitLab-330F55?style=for-the-badge&logo=gitlab&logoColor=white" alt="GitLab">
  <img src="https://img.shields.io/badge/LLM-OpenAI%20%7C%20Anthropic%20%7C%20Google-black?style=for-the-badge" alt="Multi-LLM">
  <img src="https://img.shields.io/badge/License-MIT-blue.svg?style=for-the-badge" alt="License">
</div>

<br/>

> **More than just a `.patch` reader.** AI Review Agent is an autonomous, context-aware code review assistant designed natively for GitLab Merge Requests. It clones your repository, reads the codebase, checks historical conventions, and engages in technical debates—just like a Senior Developer.

---

## 🚀 Why AI Review Agent?

Most open-source AI reviewers simply pipe your git diff into an LLM and spit out generic advice. **We do things differently:**

- 🧠 **True Contextual Awareness:** We don't just read the diff. The agent is equipped with tools (`read_file`, `search_code`, `multi_diff`) to explore the actual codebase. If you modify a function signature, it can search where else it's used before commenting. No more "hallucinated" bugs.
- ♻️ **Self-Improving Feedback Loop:** The system learns your project's conventions. A background Cron job consolidates historical AI reviews and human feedback into a tailored "Repository Best Practices" rulebook.
- 🔀 **Robust Multi-LLM Routing:** Avoid vendor lock-in. Natively supports **OpenAI (GPT-4o)**, **Anthropic (Claude 3.7)**, and **Google (Gemini 2.0)** with load-balancing and fallback mechanisms. Mix and match models based on cost, rate limits, or language proficiency.
- 💻 **Interactive Local CLI:** Don't want to spam your team with AI comments? Run the agent locally via CLI. It performs a dry-run review, displays the findings in your terminal, and lets you interactively select exactly which comments to push to GitLab.

---

## 🔍 How It Works: The "Deep Dive" Review Flow

1. **Trigger & Initialization**: A webhook catches a Merge Request event (create/update) or a manual CLI trigger is fired. A job is queued asynchronously. The specific repository config (frameworks, model overrides) is loaded.
2. **Smart Git Synchronization**: The agent acquires a lock and shallow-fetches the target branch. It calculates a smart **Base SHA** to only process _incremental_ new commits if the MR was reviewed previously, preventing noisy duplicate comments.
3. **Risk Scoring & Parsing**: Modifed files are scored for risk. Highly modified or complex files are pre-loaded directly into the LLM context. Massive PRs (>150 files) are safely truncated and sampled by risk to protect your context window.
4. **Context Gathering (The Secret Sauce)**: External data is fetched:
   - **Repository Settings**: Known frameworks/languages.
   - **Discussion History**: Previous unresolved AI comments (it can auto-resolve them if the developer fixed the code!).
   - **Feedback Rules**: Historical lessons learned specific to this repo.
5. **Agentic Code Analysis**: The LLM runs in an agentic loop. Over multiple iterations, it navigates the codebase using tools (`read_file`, `search_code`). It verifies its assumptions against real code before drafting a comment.
6. **Publish & Auto-Resolution**: Validated, structured comments are pushed as inline GitLab discussions. If a developer modified lines overlapping with a _previous_ AI comment, the agent automatically recognizes the fix and resolves the old thread.
7. **Reply Loop**: Developers can reply directly to the AI's thread in GitLab. A specialized `Replier Agent` wakes up, reads the thread history + surrounding code context, and continues the technical debate.

---

## ⚡ Quick Start

### Prerequisites

- Go 1.25.5+
- A GitLab instance (or gitlab.com)
- Access Tokens: GitLab (Personal/Project Access Token) and at least one LLM Provider (OpenAI, Anthropic, Google).

### Docker Installation (Recommended)

The easiest way to get the agent running without managing Go environments is using Docker Compose.

```bash
git clone https://github.com/antlss/gitlab-review-agent.git
cd gitlab-review-agent

# Configure your environment
cp .env.example .env
# Edit .env to set your LLM tokens and GitLab credentials

# Start the server in the background
docker-compose up -d
```

Your webhook server will be live at port `8080`. You can execute the interactive CLI directly inside the running container:

```bash
docker exec -it ai_review_agent ./cli review --project-id 123 --mr-id 45
```

### Manual Installation (From Source)

```bash
git clone https://github.com/antlss/gitlab-review-agent.git
cd gitlab-review-agent

# Build the server and CLI binaries
go build -o server ./cmd/server
go build -o cli ./cmd/cli

# Configure your environment
cp .env.example .env
```

Edit `.env` to define your `GITLAB_BASE_URL`, `GITLAB_TOKEN`, `STORE_DRIVER` (`file` or `sqlite` are easiest to start), and your preferred `LLM_DEFAULT_PROVIDER`.

### Running the Server (Webhook Mode)

Start the webhook handler and background worker pool:

```bash
./server
```

_Point your GitLab Project Webhook to `http://<your-server>:8080/webhook/gitlab`._

### Running the CLI (Interactive Dry-Run Mode)

Trigger a review manually from your terminal and pick which comments to actually post:

```bash
./cli review --project-id 123 --mr-id 45
```

You can dynamically override the model for a specific run:

```bash
./cli review --project-id 123 --mr-id 45 --model claude-3-7-sonnet-20250219
```

---

## 🏗️ Architecture Overview

- **`cmd/server`**: HTTP server handling GitLab Webhooks, Cron jobs, and Worker pools.
- **`cmd/cli`**: The Command Line Interface for interactive local reviews.
- **`internal/core`**: Heart of the logic (`review` and `reply` pipelines, `feedback` loops, `reviewer` / `replier` agents).
- **`internal/pkg`**: External port adapters (GitLab API, Git CLI wrapper, LLM drivers, SQL/File storage DAOs).

We strictly follow [Standard Go Project Layout](https://github.com/golang-standards/project-layout) conventions and utilize Clean Architecture principles.

---

## 🤝 Contributing

We welcome contributions! Please refer to the [CONTRIBUTING.md](CONTRIBUTING.md) for local development setup, coding standards, and our branch/PR workflow.

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
