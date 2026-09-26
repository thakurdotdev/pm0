// pm0 ecosystem file for the pm0.thakur.dev site.
// Usage:
//   pm0 start ecosystem.config.js
//   pm0 start ecosystem.config.js --env production
//   pm0 save && pm0 startup
module.exports = {
  apps: [
    {
      name: "pm0-site",
      script: "./server.ts",
      interpreter: "bun",
      instances: 1,
      exec_mode: "fork",
      watch: false,
      max_memory_restart: "256M",
      env: {
        PORT: 3000,
      },
      env_production: {
        PORT: 3000,
        NODE_ENV: "production",
      },
    },
  ],
};
