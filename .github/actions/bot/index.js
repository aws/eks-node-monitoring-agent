async function bot(core, github, context) {
    const payload = context.payload;

    if (!payload.comment) {
        console.log("No comment found");
        return;
    }

    const author = payload.comment.user.login;
    const authorized = ["OWNER", "MEMBER"].includes(payload.comment.author_association);
    if (!authorized) {
        console.log(`Not authorized: ${author}`);
        return;
    }

    const body = payload.comment.body.trim();
    
    // Check for /ci test command
    if (body === "/ci test") {
        console.log("Dispatching ci-test workflow");
        
        // Get PR details
        const pr = await github.rest.pulls.get({
            owner: payload.repository.owner.login,
            repo: payload.repository.name,
            pull_number: payload.issue.number
        });

        // Reply to the comment
        await github.rest.issues.createComment({
            owner: payload.repository.owner.login,
            repo: payload.repository.name,
            issue_number: payload.issue.number,
            body: `@${author} I've triggered the e2e testing workflow! 🚀`
        });

        // Dispatch the workflow
        await github.rest.actions.createWorkflowDispatch({
            owner: payload.repository.owner.login,
            repo: payload.repository.name,
            workflow_id: 'ci-test.yaml',
            ref: 'main',
            inputs: {
                pr_number: payload.issue.number.toString(),
                git_sha: pr.data.head.sha,
                requester: author
            }
        });
    }
}

module.exports = async (core, github, context) => {
    await bot(core, github, context).catch((error) => {
        core.setFailed(error);
    });
}
