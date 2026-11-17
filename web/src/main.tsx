import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./app/App";
import { liveServices, mockServices, type Services } from "./app/services";
import "./styles/folio.css";
import "./styles/tokens.css";
import "./styles/graph.css";
import "./styles/skein.css";

const root = createRoot(document.getElementById("root")!);

async function start() {
  let services: Services;
  try {
    services = import.meta.env.MODE === "mock" ? await mockServices() : liveServices();
  } catch (error) {
    root.render(
      <div className="fx t-dark sk-empty" style={{ minHeight: "100dvh" }}>
        <b>Skein could not start.</b>
        <span className="cap">{(error as Error).message}</span>
      </div>,
    );
    return;
  }
  root.render(
    <StrictMode>
      <App services={services} />
    </StrictMode>,
  );
}

void start();
