// this script cannot require/import, because it's called by actions/github-script.
// any dependencies must be passed in the inline script in action.yaml

async function bot(core, github, context, uuid) {
    const payload = context.payload;

    if (!payload.comment) {
        console.log("No comment found in payload");
        return;
    }
    console.log("Comment found in payload");

    // user's org membership must be public for the author_association to be MEMBER
    // go to the org's member page, find yourself, and set the visibility to public
    const author = payload.comment.user.login;
    const authorized = ["OWNER", "MEMBER"].includes(payload.comment.author_association);
    if (!authorized) {
        console.log(`Comment author is not authorized: ${author}`);
        return;
    }
    console.log(`Comment author is authorized: ${author}`);

    let commands;
    try {
        commands = parseCommands(uuid, payload, payload.comment.body);
    } catch (error) {
        console.log(error);
        const reply = `@${author} I didn't understand [that](${payload.comment.html_url})! 🤔\n\nTake a look at my [logs](${getBotWorkflowURL(payload, context)}).`
        replyToCommand(github, payload, reply);
        return;
    }
    if (commands.length === 0) {
        console.log("No commands found in comment body");
        return;
    }
    const uniqueCommands = [...new Set(commands.map(command => command.constructor.name))];
    if (uniqueCommands.length != commands.length) {
        replyToCommand(github, payload, `@${author} you can't use the same command more than once! 🙅`);
        return;
    }
    console.log(commands.length + " command(s) found in comment body");

    for (const command of commands) {
        const reply = await command.run(author, github);
        if (typeof reply === 'string') {
            replyToCommand(github, payload, reply);
        } else if (reply) {
            console.log(`Command returned: ${reply}`);
        } else {
            console.log("Command did not return a reply");
        }
    }
}

// replyToCommand creates a comment on the same PR that triggered this workflow
function replyToCommand(github, payload, reply) {
    github.rest.issues.createComment({
        owner: payload.repository.owner.login,
        repo: payload.repository.name,
        issue_number: payload.issue.number,
        body: reply
    });
}

// getBotWorkflowURL returns an HTML URL for this workflow execution of the bot
function getBotWorkflowURL(payload, context) {
    return `https://github.com/${payload.repository.owner.login}/${payload.repository.name}/actions/runs/${context.runId}`;
}

// parseCommands splits the comment body into lines and parses each line as a command or named arguments to the previous command.
function parseCommands(uuid, payload, commentBody) {
    const commands = [];
    if (!commentBody) {
        return commands;
    }
    const lines = commentBody.split(/\r?\n/);
    for (const line of lines) {
        console.log(`Parsing line: ${line}`);
        const command = parseCommand(uuid, payload, line);
        if (command) {
            commands.push(command);
        } else {
            const namedArguments = parseNamedArguments(line);
            if (namedArguments) {
                const previousCommand = commands.at(-1);
                if (previousCommand) {
                    if (typeof previousCommand.addNamedArguments === 'function') {
                        previousCommand.addNamedArguments(namedArguments.name, namedArguments.args);
                    } else {
                        throw new Error(`Parsed named arguments but previous command (${previousCommand.constructor.name}) does not support arguments: ${JSON.stringify(namedArguments)}`);
                    }
                } else {
                    // don't treat this as an error, because the named argument syntax might just be someone '+1'-ing.
                    console.log(`Parsed named arguments with no previous command: ${JSON.stringify(namedArguments)}`);
                }
            }
        }
    }
    return commands
}

// parseCommand parses a line as a command.
// The format of a command is `/NAME ARGS...`.
// Leading and trailing spaces are ignored.
function parseCommand(uuid, payload, line) {
    const command = line.trim().match(/^\/([a-z\-]+)(?:\s+(.+))?$/);
    if (command) {
        return buildCommand(uuid, payload, command[1], command[2]);
    }
    return null;
}

// buildCommand builds a command from a name and arguments.
function buildCommand(uuid, payload, name, args) {
    switch (name) {
        case "ci":
            return new CICommand(uuid, payload, args);
        default:
            console.log(`Unknown command: ${name}`);
            return null;
    }
}

// parseNamedArguments parses a line as named arguments.
// The format of a command is `+NAME ARGS...`.
// Leading and trailing spaces are ignored.
function parseNamedArguments(line) {
    const parsed = line.trim().match(/^\+([a-z\-]+(?::[a-z\-\d_]+)?)(?:\s+(.+))?$/);
    if (parsed) {
        return {
            name: parsed[1],
            args: parsed[2]
        }
    }
    return null;
}

class CICommand {
    workflow_goal_prefix = "workflow:";
    
    constructor(uuid, payload, args) {
        this.repository_owner = payload.repository.owner.login;
        this.repository_name = payload.repository.name;
        this.pr_number = payload.issue.number;
        this.comment_url = payload.comment.html_url;
        this.comment_created_at = payload.comment.created_at;
        this.uuid = uuid;
        this.goal = "test";
        // "test" goal is the default when no goal is specified
        // "cancel" goal cancels the most recent CI run
        if (args != null && args !== "") {
            this.goal = args;
        }
        this.goal_args = {};
    }

    addNamedArguments(name, args) {
        this.goal_args[name] = args;
    }

    async run(author, github) {
        if (this.goal === "cancel") {
            return this.cancelCI(author, github);
        }
        return this.triggerCI(author, github);
    }

    async cancelCI(author, github) {
        // List workflow runs for ci-manual.yaml
        const runs = await github.rest.actions.listWorkflowRuns({
            owner: this.repository_owner,
            repo: this.repository_name,
            workflow_id: 'ci-manual.yaml',
            status: 'in_progress'
        });

        // Find runs for this PR by checking the run name pattern "#PR_NUMBER - UUID"
        const prRuns = runs.data.workflow_runs.filter(run => 
            run.name && run.name.startsWith(`#${this.pr_number} -`)
        );

        if (prRuns.length === 0) {
            return `@${author} no running CI found for this PR.`;
        }

        // Cancel the most recent run
        const mostRecent = prRuns[0];
        await github.rest.actions.cancelWorkflowRun({
            owner: this.repository_owner,
            repo: this.repository_name,
            run_id: mostRecent.id
        });

        return `@${author} cancelled [CI run](${mostRecent.html_url}). 🛑`;
    }

    async triggerCI(author, github) {
        const pr = await github.rest.pulls.get({
            owner: this.repository_owner,
            repo: this.repository_name,
            pull_number: this.pr_number
        });
        
        let mergeable = pr.data.mergeable;
        
        // GitHub computes mergeable status asynchronously, so it may be null initially
        // Retry a few times if null
        if (mergeable === null) {
            for (let i = 0; i < 3; i++) {
                await new Promise(resolve => setTimeout(resolve, 2000)); // wait 2 seconds
                const retryPr = await github.rest.pulls.get({
                    owner: this.repository_owner,
                    repo: this.repository_name,
                    pull_number: this.pr_number
                });
                mergeable = retryPr.data.mergeable;
                if (mergeable !== null) break;
            }
        }
        
        switch (mergeable) {
            case true:
                break;
            case false:
                return `@${author} this PR is not currently mergeable, you'll need to rebase it first.`;
            case null:
                return `@${author} GitHub is still computing merge status. Please try again in a moment.`;
            default:
                throw new Error(`Unknown mergeable value: ${mergeable}`);
        }
        
        const merge_commit = await github.rest.repos.getCommit({
            owner: this.repository_owner,
            repo: this.repository_name,
            ref: pr.data.merge_commit_sha
        });
        
        if (new Date(this.comment_created_at) < new Date(merge_commit.data.commit.committer.date)) {
            return `@${author} this PR has been updated since your request, you'll need to review the changes.`;
        }
        
        const inputs = {
            uuid: this.uuid,
            pr_number: this.pr_number.toString(),
            git_sha: pr.data.merge_commit_sha,
            requester: author,
            comment_url: this.comment_url
        };
        
        // Process named arguments - workflow: prefix passes directly to workflow inputs
        for (const [name, args] of Object.entries(this.goal_args)) {
            if (name.startsWith(this.workflow_goal_prefix)) {
                inputs[name.substring(this.workflow_goal_prefix.length)] = args;
            }
        }
        
        console.log(`Dispatching workflow with inputs: ${JSON.stringify(inputs)}`);
        await github.rest.actions.createWorkflowDispatch({
            owner: this.repository_owner,
            repo: this.repository_name,
            workflow_id: 'ci-manual.yaml',
            ref: 'main',
            inputs: inputs
        });
        
        return null;
    }
}

module.exports = async (core, github, context, uuid) => {
    bot(core, github, context, uuid).catch((error) => {
        core.setFailed(error);
    });
}
