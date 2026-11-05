// Fixed addresses and credentials shared by the global setup and the tests.
// The ports sit away from wasim's defaults so a developer's own instance can
// keep running.

export const graphURL = "http://127.0.0.1:17070";
export const consoleURL = "http://127.0.0.1:17071";
export const consumerURL = "http://127.0.0.1:17080";
export const webhookURL = `${consumerURL}/webhook`;

export const appSecret = "e2e-secret";
export const verifyToken = "e2e-verify";
export const accessToken = "e2e-token";

export const graphVersion = "v20.0";
export const phoneNumberID = "100000000000001";

// Seeded users from the built-in seed.
export const ana = { waID: "15550000001", name: "Ana Lima" };
export const bruno = { waID: "15550000002", name: "Bruno Costa" };
