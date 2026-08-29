# One deployment is one trust domain

API keys carry no project binding, and `Query` accepts an empty `project_id` and answers across every project.
A reader who finds this assumes it is an authorisation bug, so state that it is not: a Kora deployment serves one trust domain, and projects scope retrieval rather than access.
Anyone holding a valid key for a deployment can read everything in it.

This follows from what Kora is for.
Its value comes from agents sharing what they learn, and the self-hosted deployment model already puts the boundary at the cluster: the operator who installs the chart decides who gets a key.
Per-project keys would add an authorisation model to every call path in exchange for a boundary the deployment topology already provides.

Two consequences worth naming.
Separate trust domains need separate deployments, which is cheap here (one chart install, one database) and is the supported answer.
And multi-tenant isolation, if it is ever wanted, is a real feature with its own design - key-to-project binding, a project-scoped query path, and audit - not a small patch to this decision.
