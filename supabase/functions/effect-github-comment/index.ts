// TODO: implement in IMPLEMENTATION_PLAN step for effect-github-comment
import "jsr:@supabase/functions-js/edge-runtime.d.ts";

Deno.serve((_req) =>
  new Response(JSON.stringify({ ok: false, error: "not implemented" }), {
    status: 501,
    headers: { "Content-Type": "application/json" },
  }),
);
